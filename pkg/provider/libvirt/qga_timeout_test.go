package libvirt

import "testing"

func TestQGACommandTimeout(t *testing.T) {
	for _, test := range []struct {
		command string
		want    string
	}{
		{command: "guest-ping", want: "10"},
		{command: "guest-file-read", want: "60"},
		{command: "guest-exec-status", want: "30"},
	} {
		t.Run(test.command, func(t *testing.T) {
			if got := qgaCommandTimeout(test.command); got != test.want {
				t.Fatalf("qgaCommandTimeout(%q) = %q, want %q", test.command, got, test.want)
			}
		})
	}
}
