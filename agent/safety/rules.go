package safety

import (
	"regexp"
	"strings"
)

// builtinRules is the policy's rule set, evaluated in order.
//
// Order is significant: the first match wins, and BLOCKED rules come first so a
// command cannot be laundered by also matching a looser SAFE pattern. Each rule
// carries the reason it exists, because a safety decision an operator cannot
// understand is one they will override.
func builtinRules() []rule {
	mk := func(name string, class Classification, reason, pattern string) rule {
		return rule{
			name:   name,
			class:  class,
			reason: reason,
			re:     regexp.MustCompile(pattern),
		}
	}

	return []rule{
		// ---- BLOCKED: unrecoverable or system-wide --------------------------
		mk("disk-and-partition", Blocked,
			"disk, partition, and filesystem-structure commands can destroy data on a drive the agent cannot reason about",
			`^\s*(diskpart|format\s+[a-z]:|mkfs|dd\s+if=.*of=/dev/|fdisk|parted|manage-bde|resize-partition)\b`),
		mk("power-and-session", Blocked,
			"power, reboot, and session commands end the operator's working session and lose the run",
			`\b(shutdown|reboot|halt|poweroff|logoff|stop-computer|restart-computer)\b`),
		mk("boot-config", Blocked,
			"boot configuration changes can render a machine unbootable",
			`\b(bcdedit|bootrecfix|sfc\s+/scannow)\b`),
		mk("credential-destruction", Blocked,
			"credential and key destruction is irreversible",
			`\b(cipher\s+/w|net\s+user\s+\S+\s+/delete|cmdkey\s+/delete|shred\s+.*-u|diskutil\s+erase)\b`),
		mk("recursive-root-delete", Blocked,
			"recursive deletion of a filesystem root or of the home directory is never a development operation",
			`\brm\s+(-[a-zA-Z]*\s+)*(-rf|-fr)\s+(/|~|\$HOME|\/\*|C:|\/home\/[a-zA-Z0-9_-]+)(\s|$)`),
		mk("windows-recursive-root-delete", Blocked,
			"recursive deletion of a drive root is never a development operation",
			`remove-item\b[^\n]*(c:\\|\\$env:userprofile)[^\n]*-recurse`),
		mk("registry-system-write", Blocked,
			"system registry writes affect the operating system rather than the project",
			`\b(reg\s+(add|delete)\s+hk(lm|ey_local_machine)|setx\s+/m)\b`),

		// ---- CONFIRM: consequential but legitimate ---------------------------
		mk("recursive-delete", Confirm,
			"recursive deletion removes work that cannot be recovered",
			`\brm\s+(-[a-zA-Z]*\s+)*-[a-zA-Z]*r|remove-item\b[^\n]*-recurse|rd\s+\/s\b|rmdir\s+\/s\b`),
		mk("force-overwrite", Confirm,
			"forced overwrite discards the previous contents without a recoverable step",
			`\b(rm\s+-f|del\s+\/f|remove-item\b[^\n]*-force|git\s+clean\s+-[a-z]*f)\b`),
		mk("git-history-destructive", Confirm,
			"history-destructive git operations discard commits that may not be pushed anywhere else",
			`\bgit\s+(reset\s+--hard|clean\s+-[a-z]*[fdx]|checkout\s+--\s|branch\s+-D|push\s+--force|push\s+-f|filter-branch)\b`),
		mk("git-publish", Confirm,
			"publishing commits affects collaborators and cannot be undone by the agent",
			`\bgit\s+(push|tag\s+-|submodule\s+sync)\b`),
		mk("network-publish", Confirm,
			"publishing to a package registry or remote affects systems outside this machine",
			`\b(npm\s+publish|yarn\s+publish|pnpm\s+publish|nuget\s+push|cargo\s+publish|twine\s+upload|gh\s+release\s+create|docker\s+(push|rm\s+-f|rmi\s+-f))\b`),
		mk("dependency-install", Confirm,
			"dependency installation runs third-party code and rewrites a lock file",
			`\b(npm\s+(i|install|ci)|yarn\s+(add|install)|pnpm\s+(add|install)|pip\s+install|uv\s+(pip\s+)?install|cargo\s+add|go\s+get|dotnet\s+(add\s+package|restore))\b`),
		mk("remote-shell", Confirm,
			"remote shell and copy commands act on other machines",
			`\b(ssh|scp|sftp|rsync|ftp|telnet)\b`),
		mk("process-kill", Confirm,
			"terminating a process may destroy unsaved state in that process",
			`\b(taskkill|kill|killall|stop-process|terminate)\b`),
		mk("package-manager-global", Confirm,
			"global package installation modifies the machine outside any project",
			`\b(npm\s+i\s+-g|pip\s+install\s+--user|choco\s+install|winget\s+install|scoop\s+install)\b`),
		mk("service-control", Confirm,
			"service control affects machine-wide state",
			`\b(net\s+stop|net\s+start|sc\s+(create|delete|config|start|stop)|systemctl\s+(stop|disable|mask)|set-service)\b`),
		mk("permission-change", Confirm,
			"permission and ownership changes can lock the operator out of their own files",
			`\b(chmod|chown|icacls|takeown|setfacl)\b`),
		mk("git-init-branch", Confirm,
			"creating or switching branches rewrites the working tree",
			`\bgit\s+(checkout\s+-[bB]|switch\s+[-c]|init|clone)\b`),
		mk("commit", Confirm,
			"committing records a change permanently in history",
			`\bgit\s+commit\b`),
		mk("sudo", Confirm,
			"privilege elevation is never performed silently",
			`^\s*(sudo|doas|runas)\b`),
		mk("write-outside-workspace", Confirm,
			"writing a file outside the workspace touches a directory this task has no context for",
			`\b(set-content|out-file|add-content|new-item|tee)\b`),

		// ---- SAFE: read-only inspection --------------------------------------
		mk("git-read", Safe, "read-only git inspection",
			`^\s*git\s+(status|diff|log|show|branch|rev-parse|describe|blame|ls-files|ls-tree|shortlog|config\s+--get|remote\s+-v|tag\s*$|stash\s+list|reflog)\b`),
		mk("git-no-verify-write", Safe, "staging area inspection only",
			`^\s*git\s+(add\s+(-n|--dry-run))\b`),
		mk("filesystem-list", Safe, "directory listing",
			`^\s*(ls|dir|find|fd|rg|grep|ag|wc|stat|file|du|tree)\b`),
		mk("filesystem-read", Safe, "file content inspection",
			`^\s*(cat|type|head|tail|less|more|bat|wc|sort|uniq|diff|cmp)\b`),
		mk("go-toolchain", Safe, "Go build, test, and analysis commands do not modify source",
			`^\s*go\s+(build|test|vet|list|env|doc|run|version|tool|generate|fmt|clean)\b`),
		mk("go-modules", Safe, "module commands may rewrite go.mod/go.sum inside the workspace and fetch dependencies",
			`^\s*go\s+mod\s+(tidy|verify|download|graph|why)\b`),
		mk("python-analysis", Safe, "Python analysis and test runners",
			`^\s*(pytest|py\.test|ruff|black\s+--check|mypy|pylint|pyright|tox\s+-e|python\s+-m\s+(pytest|unittest|compileall|json\.tool|ruff|mypy|py_compile|pip\s+show))\b`),
		mk("node-analysis", Safe, "Node and TypeScript analysis and test runners",
			`^\s*(npm\s+(test|run\s+(build|lint|typecheck|test|format))\b|npx\s+(tsc|eslint|prettier)\b|tsc|vitest|jest|eslint\s+--version|prettier\s+--check)`),
		// Interpreters running an arbitrary script execute arbitrary code, which
		// is indistinguishable from running a command. They are therefore
		// confirmation-class rather than allow-listed: running a project's own
		// test suite is a named subcommand and stays unattended, while handing
		// an interpreter an inline script asks the operator first.
		mk("interpreter-inline", Confirm,
			"an inline interpreter invocation executes arbitrary code that cannot be reviewed by name",
			`^\s*(python|python3|py|node|perl|ruby|php)\s+(-[a-zA-Z]+\s+)*(-c|-e|--eval|-\w)`),
		mk("dotnet-toolchain", Safe, "dotnet build and test commands",
			`^\s*dotnet\s+(build|test|run\s+--no-launch-profile|list\s+(package|project)|format\s+--verify-no-changes)\b`),
		mk("rust-toolchain", Safe, "cargo build and test commands",
			`^\s*(cargo\s+(build|test|check|clippy|fmt|run|metadata|locate-project)|rustc|rustfmt)\b`),
		mk("java-toolchain", Safe, "Java and Kotlin build tools",
			`^\s*(mvn|gradle|gradlew|javac|java\s+-version|ktlint)\b`),
		mk("make-toolchain", Safe, "make targets are project-defined build steps",
			`^\s*(make|cmake|ctest|ninja|meson)\b`),
		mk("http-read", Safe, "read-only HTTP retrieval",
			`^\s*(curl\s+(-[a-zA-Z]+\s+)*-(I|head|s)|iwr\s+.*-usebasicparsing)\b`),
		mk("echo", Safe, "echo produces no side effect",
			`^\s*echo\b`),
		mk("env-info", Safe, "environment inspection",
			`^\s*(where|which|type\s+-p|set|env|printenv|uname|whoami|hostname|ver|systeminfo)\b`),
		mk("version", Safe, "version queries",
			`^\s*[\w.-]+\s+(--version|-v|-version|version)\b`),
	}
}

// ClassifyFragment classifies a single command segment. It is exported so the
// executor can re-check each segment it actually runs after shell expansion,
// which is stricter than classifying the original line.
func (p *Policy) ClassifyFragment(fragment string) Verdict {
	return p.classifySegment(strings.TrimSpace(fragment))
}