# agentd Makefile — build, deploy, operate
#
# Deploy config from .env (gitignored):
#   DEPLOY_HOST (default: chromabook)
#   DEPLOY_PORT (default: 2535)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X github.com/safety-quotient-lab/agentd.Version=$(VERSION)"

REMOTE_BIN    := /home/kashif/platform/agentd
REMOTE_BACKUP := /home/kashif/platform/agentd-backup-$(shell date +%Y%m%d-%H%M)

# Load .env if present
-include .env
DEPLOY_HOST ?= chromabook
DEPLOY_PORT ?= 2535
SSH_CMD = ssh -p $(DEPLOY_PORT) $(DEPLOY_HOST)
SCP_CMD = scp -P $(DEPLOY_PORT)

# Agent systemd units — one per agent
AGENT_UNITS := agentd-psychology agentd-psq agentd-observatory agentd-unratified

.PHONY: build deploy deploy-transfer deploy-restart deploy-validate status clean help

# ── Build ─────────────────────────────────────────────────────
build:
	@echo "Building agentd $(VERSION) (linux/amd64 + darwin/arm64)..."
	@GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o agentd-linux ./cmd/agentd/
	@go build $(LDFLAGS) -o agentd ./cmd/agentd/
	@echo "  Linux:  ./agentd-linux ($$(du -h agentd-linux | cut -f1))"
	@echo "  Darwin: ./agentd ($$(du -h agentd | cut -f1))"

# ── Deploy ────────────────────────────────────────────────────
deploy: build deploy-transfer deploy-restart deploy-validate
	@$(SSH_CMD) "echo $(VERSION) > /home/kashif/platform/.agentd-version"
	@echo ""
	@echo "Deploy complete ($(VERSION))."

deploy-transfer:
	@echo ""
	@echo "═══ Transferring agentd binary ═══"
	@$(SCP_CMD) ./agentd-linux $(DEPLOY_HOST):$(REMOTE_BIN).new
	@echo "  Transferred to $(REMOTE_BIN).new"

deploy-restart:
	@echo ""
	@echo "═══ Restarting agentd processes ═══"
	@$(SSH_CMD) '\
		echo "  Stopping agent units..." && \
		systemctl --user --no-block stop $(AGENT_UNITS) 2>/dev/null; \
		sleep 2 && \
		echo "  Swapping binary..." && \
		cp $(REMOTE_BIN) $(REMOTE_BACKUP) 2>/dev/null; \
		mv $(REMOTE_BIN).new $(REMOTE_BIN) && chmod +x $(REMOTE_BIN) && \
		echo "  Starting agent units..." && \
		systemctl --user --no-block start $(AGENT_UNITS) && \
		sleep 3 && \
		echo "" && echo "  Processes:" && \
		pgrep -f "/home/kashif/platform/agentd" -la 2>/dev/null | head -6'

deploy-validate:
	@echo ""
	@echo "═══ Post-deploy validation ═══"
	@sleep 5
	@curl -sf https://psychology-agent.safety-quotient.dev/health && echo "  psychology-agent: OK" || echo "  psychology-agent: FAILED"
	@curl -sf https://psq-agent.safety-quotient.dev/health && echo "  psq-agent: OK" || echo "  psq-agent: FAILED"

# ── Operations ────────────────────────────────────────────────
status:
	@$(SSH_CMD) 'pgrep -f "platform/agentd" -la'

clean:
	@rm -f agentd agentd-linux agentd-darwin

help:
	@echo "agentd Makefile ($(VERSION))"
	@echo ""
	@echo "  make build    Build linux/amd64 + darwin/arm64"
	@echo "  make deploy   Build + transfer + restart + validate"
	@echo "  make status   Show running agentd processes on $(DEPLOY_HOST)"
	@echo "  make clean    Remove built binaries"
