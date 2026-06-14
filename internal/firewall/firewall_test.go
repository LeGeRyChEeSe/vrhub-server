package firewall

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestRuleName(t *testing.T) {
	cases := []struct {
		port int
		want string
	}{
		{39457, "VRHub Server (TCP/39457)"},
		{8080, "VRHub Server (TCP/8080)"},
		{0, "VRHub Server (TCP/0)"},
	}
	for _, c := range cases {
		if got := RuleName(c.port); got != c.want {
			t.Errorf("RuleName(%d) = %q, want %q", c.port, got, c.want)
		}
	}
}

// On any non-Windows runner, EnsureOpen and Remove must be no-ops —
// they are called unconditionally from main.go and must not break
// `go test` on Linux CI.
func TestEnsureRemoveOpen_NonWindowsNoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-Windows only; Windows runner uses real netsh")
	}
	if err := EnsureOpen(39457); err != nil {
		t.Fatalf("EnsureOpen on %s: %v", runtime.GOOS, err)
	}
	if err := Remove(39457); err != nil {
		t.Fatalf("Remove on %s: %v", runtime.GOOS, err)
	}
}

func TestIsNotFoundErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "english-no-rules-match",
			err:  &netshErr{out: "No rules match the specified criteria."},
			want: true,
		},
		{
			name: "french-aucune-regle",
			err:  &netshErr{out: "Aucune règle ne correspond aux critères spécifiés."},
			want: true,
		},
		{
			name: "french-critere-trouve",
			err:  &netshErr{out: "Le critère specificié n'a pas été trouvé."},
			want: true,
		},
		{
			name: "unrelated-error",
			err:  &netshErr{out: "The data is invalid."},
			want: false,
		},
		{
			name: "nil",
			err:  nil,
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isNotFoundErr(c.err); got != c.want {
				t.Errorf("isNotFoundErr(%q) = %v, want %v", errString(c.err), got, c.want)
			}
		})
	}
}

func TestIsAlreadyExistsErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "english-already-exists",
			err:  &netshErr{out: "A rule with the specified name already exists."},
			want: true,
		},
		{
			name: "french-existe-deja",
			err:  &netshErr{out: "Une règle avec le nom spécifié existe déjà."},
			want: true,
		},
		{
			name: "unrelated-error",
			err:  &netshErr{out: "The data is invalid."},
			want: false,
		},
		{
			name: "nil",
			err:  nil,
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isAlreadyExistsErr(c.err); got != c.want {
				t.Errorf("isAlreadyExistsErr(%q) = %v, want %v", errString(c.err), got, c.want)
			}
		})
	}
}

func TestClassifyNetshErr_AdminRequired(t *testing.T) {
	cases := []string{
		// English (Windows en-US).
		"The requested operation requires elevation (Run as administrator).",
		// French (Windows fr-FR).
		"L'opération demandée nécessite une élévation (Exécuter en tant qu'administrateur).",
	}
	for _, msg := range cases {
		in := &netshErr{args: []string{"test"}, out: msg, err: errors.New("exit status 1")}
		out := classifyNetshErr(in)
		if out == nil {
			t.Errorf("classifyNetshErr(%q) returned nil; expected admin-required wrapper", msg)
			continue
		}
		if !strings.Contains(out.Error(), "admin rights required") {
			t.Errorf("classifyNetshErr(%q) = %q, expected to mention 'admin rights required'", msg, out.Error())
		}
		// Unwrap chain must still expose the original netshErr so
		// callers can inspect ExitError / Output if they need to.
		if !errors.Is(out, in.err) {
			t.Errorf("classifyNetshErr(%q) lost the original error in the Unwrap chain", msg)
		}
	}
}

func TestClassifyNetshErr_PassThrough(t *testing.T) {
	in := &netshErr{args: []string{"x"}, out: "some other failure", err: errors.New("boom")}
	out := classifyNetshErr(in)
	if out != in {
		t.Errorf("classifyNetshErr should pass through unrelated errors unchanged, got %v", out)
	}
	if classifyNetshErr(nil) != nil {
		t.Errorf("classifyNetshErr(nil) must be nil")
	}
}

func TestNetshErr_ErrorFormat(t *testing.T) {
	e := &netshErr{
		args: []string{"advfirewall", "firewall", "add", "rule"},
		out:  "boom",
		err:  errors.New("exit status 1"),
	}
	s := e.Error()
	for _, want := range []string{"netsh", "advfirewall firewall add rule", "exit status 1", "boom"} {
		if !strings.Contains(s, want) {
			t.Errorf("netshErr.Error() = %q, missing %q", s, want)
		}
	}
	// Unwrap must return the underlying exec error.
	if !errors.Is(e, e.err) {
		t.Errorf("netshErr.Unwrap() = %v, want %v", e.Unwrap(), e.err)
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
