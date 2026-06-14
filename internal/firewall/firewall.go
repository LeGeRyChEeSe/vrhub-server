// Package firewall opens and removes a TCP port in the host firewall so
// the VRHub server is reachable from the LAN without manual user setup
// (the Windows "Windows Security Alert" popup that blocks inbound
// connections until the operator clicks "Allow").
//
// Behaviour:
//   - On Windows: delegates to `netsh advfirewall firewall`. The rules
//     are scoped by port and named "VRHub Server (TCP/<port>)" so they
//     can be removed later and never collide with other software.
//   - On Linux / macOS / other: a no-op. Operators are expected to use
//     their distro's firewall tooling (ufw / firewalld / iptables) or
//     to run the server in a container with the port published.
//
// All exported functions are best-effort: a failure to mutate the
// firewall returns an error, but the caller is expected to log and
// continue — the server should still come up so the operator can fix
// the firewall through the admin UI on 127.0.0.1 (which is always
// reachable, regardless of inbound rules).
package firewall

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// RuleName returns the canonical firewall rule name used for the given
// port. Centralised so EnsureOpen and Remove always reference the same
// string and a typo can't silently leak a rule.
func RuleName(port int) string {
	return fmt.Sprintf("VRHub Server (TCP/%d)", port)
}

// EnsureOpen creates (or refreshes) the firewall rule that allows
// inbound TCP traffic to the given port. Idempotent: if a rule with
// the same name already exists, it is deleted first and recreated so a
// port change (e.g. settings page rebind) takes effect on the next
// call.
//
// Returns nil on non-Windows platforms (no-op).
//
// On Windows a non-nil error is returned when the add operation fails
// for a reason other than "the rule already exists" (which is treated
// as success). The most common failure mode is running without admin
// rights — netsh returns "requires elevation" and the OS rejects the
// change. The error message is human-readable and tells the operator
// how to recover.
func EnsureOpen(port int) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	name := RuleName(port)

	// Best-effort pre-delete: we want to refresh the rule on every
	// start (port changes via the settings page reuse the same rule
	// name) so any leftover rule from a previous install is removed
	// before the add. On a non-elevated process this also fails with
	// "requires elevation" — we swallow that case because the add
	// below will surface the same error in its own (more useful)
	// error message.
	_ = runNetsh("advfirewall", "firewall", "delete", "rule",
		fmt.Sprintf("name=%s", name),
	)

	if err := runNetsh("advfirewall", "firewall", "add", "rule",
		fmt.Sprintf("name=%s", name),
		"dir=in",
		"action=allow",
		"protocol=TCP",
		fmt.Sprintf("localport=%d", port),
		"profile=any",
	); err != nil {
		if isAlreadyExistsErr(err) {
			// Another process (or a previous run) already created
			// the rule — that's exactly what we wanted. Not an
			// error.
			return nil
		}
		return fmt.Errorf("firewall: open TCP/%d: %w", port, classifyNetshErr(err))
	}
	return nil
}

// Remove deletes the firewall rule previously created by EnsureOpen for
// the given port. Called on graceful shutdown so a server that
// uninstalls cleanly doesn't leave an orphan rule behind.
//
// Returns nil if the rule didn't exist (e.g. a previous EnsureOpen
// failed and the user fixed the firewall manually).
func Remove(port int) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	name := RuleName(port)
	if err := runNetsh("advfirewall", "firewall", "delete", "rule",
		fmt.Sprintf("name=%s", name),
	); err != nil {
		if isNotFoundErr(err) {
			return nil
		}
		return fmt.Errorf("firewall: remove %s: %w", name, classifyNetshErr(err))
	}
	return nil
}

// runNetsh invokes netsh with the given args and returns a wrapped
// error capturing the exit code plus combined stdout/stderr.
//
// We deliberately do NOT use exec.CommandContext with a timeout: the
// firewall helper is called from the shutdown path, and a hung netsh
// must not stall the process exit. netsh typically completes in <100ms.
func runNetsh(args ...string) error {
	cmd := exec.Command("netsh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &netshErr{args: args, out: string(out), err: err}
	}
	return nil
}

type netshErr struct {
	args []string
	out  string
	err  error
}

func (e *netshErr) Error() string {
	return fmt.Sprintf("netsh %s: %v: %s",
		strings.Join(e.args, " "), e.err, strings.TrimSpace(e.out))
}

func (e *netshErr) Unwrap() error { return e.err }

// classifyNetshErr translates the most common netsh failure modes into
// a human-friendly message. Specifically: "Run as administrator" /
// "requires elevation" — the operator is on a non-elevated prompt and
// the OS rejected the rule change. The fix is to either relaunch the
// server elevated or to add the rule manually via the Windows
// Firewall control panel.
func classifyNetshErr(err error) error {
	if err == nil {
		return nil
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "requires elevation") ||
		strings.Contains(s, "run as administrator") ||
		strings.Contains(s, "exécuter en tant qu'administrateur"):
		return fmt.Errorf("admin rights required (%w); restart the server elevated or add a manual inbound allow rule for the TCP port", err)
	}
	return err
}

// isNotFoundErr reports whether err is the netsh "no rules match the
// specified criteria" error we get when deleting a rule that doesn't
// exist. Matched substrings cover both the English and the localised
// French netsh output (the French build emits the typo "spécific"
// / "spécifiées" / "critères" depending on the Windows SKU).
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "no rules match") {
		return true
	}
	if strings.Contains(s, "aucune règle ne correspond") {
		return true
	}
	// netsh's French localisation has had typos across Windows
	// versions ("spécifié" vs "specific"). Match the criterion
	// fragments instead of the exact wording so we catch both.
	if strings.Contains(s, "critère") && strings.Contains(s, "trouvé") {
		return true
	}
	return false
}

// isAlreadyExistsErr reports whether err is the netsh "A rule with the
// specified name already exists" message we get on `add rule` when
// another process (or a previous run we couldn't delete because of
// missing admin rights) already created the rule. We treat that as
// success: the operator-visible state is exactly what we wanted.
func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "already exists") {
		return true
	}
	if strings.Contains(s, "existe déjà") {
		return true
	}
	return false
}
