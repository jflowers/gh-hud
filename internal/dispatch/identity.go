package dispatch

// Which GitHub account a dispatched agent acts as. Everything runs as the
// operator's own gh login except reviews in botOrg, which run as the
// uf-divisor machine account: the pane exports GH_TOKEN from botTokenVar
// and puts a gh guard first on PATH.
//
// The token lives in agent-env on every machine, so an agent could read it
// whatever gh-hud does, and the guard only sees gh; both are conventions
// that keep a well-behaved run honest, not a security boundary. The token
// value never passes through gh-hud: only shell lines naming the variable
// are built here, so it stays out of pane transcripts, argv and logs.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const (
	botOrg      = "unbound-force"
	botLogin    = "uf-divisor"
	botTokenVar = "UF_DIVISOR_GH_TOKEN"

	// botGuardDir holds the gh guard, on every machine. Written for the
	// shell: $HOME is expanded where the line runs.
	botGuardDir = `$HOME/.local/share/gh-hud/guard`

	// botLoad finds the token in a shell that doesn't have it: a mac login
	// bash never reads env.sh, so a hub pane only has what the herdr server
	// was started with. Reading agent-env here means a new token needs no
	// server restart.
	botLoad = `if [ -z "$` + botTokenVar + `" ] && [ -r "$HOME/.config/agent-env" ]; then . "$HOME/.config/agent-env"; fi`

	// botExport switches the shell to the bot account.
	botExport = `export GH_TOKEN="$` + botTokenVar + `" PATH="` + botGuardDir + `:$PATH"`

	// botReviewNote is appended to the review command's arguments. The
	// command picks the review event from the council verdict and only
	// downgrades it on the poster's own PR, which never matches here: the
	// poster is the bot, so an APPROVE would be real and count toward
	// required approvals. The guard backs this up. It reaches the pane's
	// shell, so it holds no quotes or shell metacharacters.
	botReviewNote = "Post this review with event COMMENT whatever the council verdict, and open the body with the verdict. " +
		"Never post APPROVE or REQUEST_CHANGES and never merge: gh-hud runs this as the " + botLogin + " account, whose approval would count."
)

// errBotToken marks a dispatch refused because the bot token isn't on the
// machine; running anyway would post the review as the operator.
var errBotToken = errors.New(botTokenVar + " is missing or empty")

// runsAsBot reports whether the task runs as the bot account: a review of
// a PR in botOrg, whoever opened it. GitHub owners are case-insensitive.
func runsAsBot(task Task, repo string) bool {
	owner, _, ok := strings.Cut(repo, "/")
	return ok && task == TaskReview && strings.EqualFold(owner, botOrg)
}

// botGuard is the gh first on PATH in a bot dispatch. It refuses merges and
// any review event but COMMENT, then hands everything else to the real gh.
// A refusal reads as an HTTP 422 because /uf.review-pr answers that by
// reposting as COMMENT. Unknown payloads fail closed.
const botGuard = `#!/bin/sh
# gh-hud guard for uf-divisor dispatches. Written by gh-hud; do not edit.
guard=${0%/*}

refuse() {
	echo "gh: HTTP 422: $1 [gh-hud guard: ` + botLogin + ` posts COMMENT reviews only and never merges; resend with event COMMENT]" >&2
	exit 1
}

# Every "event" key in a JSON file must be COMMENT, and there must be one.
# Quotes inside JSON strings are escaped, so body text can't match.
event_ok() {
	events=$(tr -d ' \t\r\n' <"$1" | grep -o '"event":"[^"]*"') || return 1
	[ "$(printf '%s\n' "$events" | sort -u)" = '"event":"COMMENT"' ]
}

check_pr() {
	shift
	while :; do
		case ${1:-} in
		-R | --repo) shift 2 ;;
		--repo=*) shift ;;
		*) break ;;
		esac
	done
	case ${1:-} in
	merge) refuse "gh pr merge is blocked" ;;
	review) ;;
	*) return 0 ;;
	esac
	comment=
	for a in "$@"; do
		case $a in
		--approve | -a | --request-changes | -r) refuse "gh pr review $a is blocked" ;;
		--comment | -c) comment=1 ;;
		esac
	done
	[ -n "$comment" ] || refuse "gh pr review needs --comment"
}

check_api() {
	method= input= event= fields= prev=
	for a in "$@"; do
		case $prev in
		-X | --method) method=$a ;;
		--input) input=$a ;;
		-f | -F | --field | --raw-field)
			fields=1
			case $a in event=*) event=${a#event=} ;; esac
			;;
		esac
		case $a in
		--method=*) method=${a#--method=} ;;
		--input=*) input=${a#--input=} ;;
		--field=* | --raw-field=*)
			fields=1
			a=${a#*=}
			case $a in event=*) event=${a#event=} ;; esac
			;;
		esac
		prev=$a
	done
	method=$(printf %s "$method" | tr '[:lower:]' '[:upper:]')
	case $method in
	GET | HEAD) return 0 ;;
	"") [ -n "$fields$input" ] || return 0 ;;
	esac
	args=" $* "
	case $args in
	*" graphql "*)
		text=$args
		case $args in *=@*) refuse "graphql from a file is blocked" ;; esac
		if [ -n "$input" ]; then
			[ -r "$input" ] || refuse "graphql input is not a readable file"
			text=$(cat "$input")
		fi
		case $text in
		*addPullRequestReview* | *submitPullRequestReview* | *mergePullRequest* | *PullRequestAutoMerge* | *mergeBranch*)
			refuse "graphql review and merge mutations are blocked"
			;;
		esac
		;;
	*/merge" "* | */merge\?* | */merges" "*) refuse "the merge API is blocked" ;;
	*pulls/*/reviews" "* | *pulls/*/reviews\?* | *pulls/*/events" "*)
		if [ -n "$input" ]; then
			[ "$input" != - ] && [ -r "$input" ] || refuse "review payload must be a readable file"
			event_ok "$input" || refuse "review event must be COMMENT"
		elif [ "$event" != COMMENT ]; then
			refuse "review event must be COMMENT"
		fi
		;;
	esac
}

case ${1:-} in
pr) check_pr "$@" ;;
api) check_api "$@" ;;
esac

saved=$IFS
IFS=:
for p in $PATH; do
	[ "$p" != "$guard" ] && [ -x "$p/gh" ] && real=$p/gh && break
done
IFS=$saved
[ -n "${real:-}" ] || { echo "gh-hud guard: no gh on PATH" >&2; exit 127; }
exec "$real" "$@"
`

// botPrepareScript reports whether the machine has the token and installs
// the guard. It prints only "ok" or "missing".
func botPrepareScript() string {
	return botLoad + `
if [ -z "$` + botTokenVar + `" ]; then echo missing; exit 0; fi
mkdir -p "` + botGuardDir + `" || exit 1
cat >"` + botGuardDir + `/gh.$$" <<'GHHUD_GUARD' || exit 1
` + botGuard + `GHHUD_GUARD
chmod 755 "` + botGuardDir + `/gh.$$" && mv -f "` + botGuardDir + `/gh.$$" "` + botGuardDir + `/gh" && echo ok
`
}

// prepareBot gets machine m ready for a bot dispatch. It runs before
// preflight so a machine without the token refuses before anything touches
// the checkout, and placement moves on to the next candidate. The pane
// repeats the token check, since only the pane's own shell is authoritative.
func prepareBot(ctx context.Context, m Machine, ref string) error {
	out, err := m.Run(ctx, "", "sh", "-c", botPrepareScript())
	if err != nil {
		if !m.Local {
			return fmt.Errorf("%w: %v", errMachineUnreachable, err)
		}
		return fmt.Errorf("prepare %s review: %w", botLogin, err)
	}
	if strings.TrimSpace(out) != "ok" {
		return botTokenError(m, ref)
	}
	return nil
}

func botTokenError(m Machine, ref string) error {
	return fmt.Errorf("%w on %s: not reviewing %s, it would post as you instead of %s", errBotToken, machineLabel(m), ref, botLogin)
}

// botCommand wraps a local-runner command so it runs as the bot, through
// the same shell lines as a pane, and exits rather than run as the operator.
func botCommand(bin string, argv []string) *exec.Cmd {
	script := botLoad + `; [ -n "$` + botTokenVar + `" ] || { echo "` + botTokenVar + ` is missing or empty" >&2; exit 78; }; ` + botExport + `; exec "$0" "$@"`
	return exec.Command("sh", append([]string{"-c", script, bin}, argv...)...)
}

// autoFailNote is the one-line notice for an auto-dispatch that failed for
// want of the token, which would otherwise pass unseen: other auto failures
// are routine (busy machines) and retried quietly. Each ref is reported
// once per failure streak, not on every refresh.
func (d *Dispatcher) autoFailNote(task Task, ref string, err error) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err == nil {
		delete(d.botWarned, ref)
		return "", false
	}
	if !errors.Is(err, errBotToken) || d.botWarned[ref] {
		return "", false
	}
	if d.botWarned == nil {
		d.botWarned = map[string]bool{}
	}
	d.botWarned[ref] = true
	return fmt.Sprintf("auto %s %s refused: %s missing on every machine that could take it", task, ref, botTokenVar), true
}
