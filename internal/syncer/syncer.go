// Package syncer orchestrates the autonomous sync cycle.
// Replaces autonomous-sync.sh's main() function: git pull → budget check →
// triage → orientation → claude /sync → git push.
package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/safety-quotient-lab/agentd/internal/budget"
	"github.com/safety-quotient-lab/agentd/internal/crossrepo"
	"github.com/safety-quotient-lab/agentd/internal/db"
	"github.com/safety-quotient-lab/agentd/internal/heartbeat"
	"github.com/safety-quotient-lab/agentd/internal/orientation"
	"github.com/safety-quotient-lab/agentd/internal/triage"
)

// Config holds syncer parameters.
type Config struct {
	AgentID             string
	ProjectRoot         string
	MaxTurns            int
	MaxConsecutiveErrors int
	AllowedTools        string
}

// DefaultConfig returns sensible syncer defaults.
func DefaultConfig(agentID, projectRoot string) Config {
	return Config{
		AgentID:             agentID,
		ProjectRoot:         projectRoot,
		MaxTurns:            80,
		MaxConsecutiveErrors: 2,
		AllowedTools:        "Read,Write,Edit,Glob,Grep,Bash",
	}
}

// Syncer orchestrates the autonomous sync cycle.
type Syncer struct {
	config  Config
	db      *db.DB
	localDB *db.DB
	budget  *budget.Manager
}

// New creates a syncer with the given configuration.
func New(config Config, database, localDB *db.DB) *Syncer {
	return &Syncer{
		config:  config,
		db:      database,
		localDB: localDB,
		budget:  budget.New(config.AgentID, database, localDB),
	}
}

// RunSync executes one complete sync cycle. Called by the oscillator
// when activation exceeds threshold.
func (s *Syncer) RunSync(ctx context.Context) error {
	start := time.Now()
	slog.Info("sync cycle starting", "component", "syncer")

	// 1. Budget check — halt if exhausted
	status, err := s.budget.Check()
	if err != nil {
		slog.Error("sync HALT", "component", "syncer", "error", err)
		return err
	}
	if status.Sedated {
		slog.Info("agent sedated — skipping sync", "component", "syncer")
		return nil
	}
	slog.Info("budget check passed", "component", "syncer",
		"spent", status.Spent, "cutoff", status.Cutoff)

	// 2. Interval check — defer if too soon
	allowed, remaining := s.budget.CheckInterval(false)
	if !allowed {
		slog.Info("sync DEFER — interval not elapsed", "component", "syncer", "remaining", remaining)
		return nil
	}

	// 3. Emit heartbeat (mesh presence)
	if err := heartbeat.Emit(s.config.ProjectRoot, s.config.AgentID); err != nil {
		slog.Warn("heartbeat emit failed", "component", "syncer", "error", err)
	}

	// 4. Git pull
	if err := s.gitPull(ctx); err != nil {
		slog.Warn("git pull failed", "component", "syncer", "error", err)
		// Non-fatal — continue with local state
	}

	// 5. Cross-repo fetch — get messages from peer repositories
	crConfig := crossrepo.Config{ProjectRoot: s.config.ProjectRoot, AgentID: s.config.AgentID}
	fetchResults := crossrepo.Fetch(crConfig, s.db)
	totalNew := 0
	for _, r := range fetchResults {
		totalNew += r.NewMessages
	}
	if totalNew > 0 {
		slog.Info("cross-repo fetch completed", "component", "syncer",
			"new_messages", totalNew, "peers", len(fetchResults))
	}

	// 6. Triage — auto-process trivial messages
	triageResult, err := triage.Scan(s.db)
	if err != nil {
		slog.Warn("triage failed", "component", "syncer", "error", err)
	}

	// 5. Pre-flight check — skip claude if nothing needs LLM
	if triageResult.NeedsLLM == 0 && !triage.HasSubstance(s.db) {
		slog.Info("sync NO-OP — all messages handled deterministically", "component", "syncer")
		s.gitPush(ctx) // push any triage changes
		s.budget.ResetConsecutiveBlocks()
		slog.Info("sync cycle complete (no-op)", "component", "syncer",
			"duration", time.Since(start).Round(time.Millisecond))
		return nil
	}

	// 6. Generate orientation payload (native Go — replaces orientation-payload.py)
	orientationText := orientation.Generate(s.db, s.config.AgentID)

	// 7. Run claude /sync
	syncStart := time.Now()
	output, err := s.runClaude(ctx, orientationText)
	syncDuration := time.Since(syncStart)

	if err != nil {
		slog.Error("claude failed", "component", "syncer",
			"duration", syncDuration, "error", err)

		blocks := s.budget.IncrementConsecutiveBlocks()
		if blocks >= s.config.MaxConsecutiveErrors {
			slog.Error("sync HALT — consecutive error limit reached",
				"component", "syncer", "consecutive_errors", blocks)
		}

		s.budget.RecordAction("sync", fmt.Sprintf("sync failed (%s)", syncDuration), 1)
		return fmt.Errorf("claude sync failed: %w", err)
	}

	slog.Info("claude completed", "component", "syncer",
		"duration", syncDuration, "output_bytes", len(output))

	// 8. Record success + push
	s.budget.RecordAction("sync",
		fmt.Sprintf("sync completed (%s)", syncDuration.Round(time.Millisecond)), 1)
	s.budget.ResetConsecutiveBlocks()

	if err := s.gitPush(ctx); err != nil {
		slog.Warn("git push failed", "component", "syncer", "error", err)
		s.budget.RecordAction("git_push", "push failed after sync", 1)
	}

	slog.Info("sync cycle complete", "component", "syncer",
		"budget_spent", status.Spent+1, "duration", time.Since(start).Round(time.Millisecond))
	return nil
}

// gitPull fetches and rebases from origin.
func (s *Syncer) gitPull(ctx context.Context) error {
	// Commit any dirty tracked files first (pre-pull cleanup)
	s.runGit(ctx, "add", "-u")
	s.runGit(ctx, "diff", "--cached", "--quiet")
	// If staged changes exist, commit them
	cmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--quiet")
	cmd.Dir = s.config.ProjectRoot
	if err := cmd.Run(); err != nil {
		// Has staged changes — commit
		s.runGit(ctx, "commit", "-m", "autonomous: pre-pull commit\n\nCo-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>")
	}

	// Fetch and rebase
	if _, err := s.runGitOutput(ctx, "pull", "--rebase", "origin", "main"); err != nil {
		return fmt.Errorf("git pull: %w", err)
	}
	return nil
}

// gitPush pushes to origin.
func (s *Syncer) gitPush(ctx context.Context) error {
	// Check if there are unpushed commits
	head, _ := s.runGitOutput(ctx, "rev-parse", "HEAD")
	remote, _ := s.runGitOutput(ctx, "rev-parse", "origin/main")
	if strings.TrimSpace(head) == strings.TrimSpace(remote) {
		return nil // nothing to push
	}

	if _, err := s.runGitOutput(ctx, "push", "origin", "main"); err != nil {
		return fmt.Errorf("git push: %w", err)
	}
	return nil
}

// runClaude invokes claude -p with the sync prompt.
func (s *Syncer) runClaude(ctx context.Context, orientation string) (string, error) {
	prompt := "/sync"
	if orientation != "" {
		prompt = orientation + "\n\n/sync"
	}

	cmd := exec.CommandContext(ctx, "claude", "-p", prompt,
		"--allowedTools", s.config.AllowedTools,
		"--permission-mode", "bypassPermissions",
		"--max-turns", fmt.Sprintf("%d", s.config.MaxTurns))
	cmd.Dir = s.config.ProjectRoot

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check for rate limiting
		outStr := string(output)
		if isRateLimited(outStr) {
			return outStr, fmt.Errorf("rate limited")
		}
		// Check for max-turns (partial success)
		if isMaxTurns(outStr) {
			slog.Warn("hit max-turns — partial sync", "component", "syncer")
			return outStr, nil // partial success, not failure
		}
		return outStr, fmt.Errorf("claude exit: %w", err)
	}
	return string(output), nil
}

func (s *Syncer) runGit(ctx context.Context, args ...string) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.config.ProjectRoot
	cmd.Run() // ignore errors for non-critical git ops
}

func (s *Syncer) runGitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.config.ProjectRoot
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func isRateLimited(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "usage limit") ||
		strings.Contains(lower, "429") ||
		strings.Contains(lower, "you've hit your limit")
}

func isMaxTurns(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "max turns") ||
		strings.Contains(lower, "reached max")
}
