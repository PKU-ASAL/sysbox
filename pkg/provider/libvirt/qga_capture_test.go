package libvirt

import (
	"strings"
	"testing"
)

func TestFileCaptureQuotesNativeArgumentsWithoutBatchExpansion(t *testing.T) {
	for _, arg := range []string{`C:\Program Files\SysArmor\agent.exe`, `state`, `C:\path\`, `a & b`} {
		quoted, err := windowsBatchArg(arg)
		if err != nil || !strings.HasPrefix(quoted, `"`) || !strings.HasSuffix(quoted, `"`) {
			t.Fatalf("%q: %q %v", arg, quoted, err)
		}
	}
	quoted, err := windowsBatchArg(`C:\path\`)
	if err != nil || quoted != `"C:\path\\"` {
		t.Fatalf("trailing slash: %q %v", quoted, err)
	}
	for _, arg := range []string{"a\"b", "%PATH%", "a\nb", "a\rb"} {
		if _, err := windowsBatchArg(arg); err == nil {
			t.Fatalf("accepted batch expansion: %q", arg)
		}
	}
}
