package automation

import "testing"

func TestPackageChangeActionForTask(t *testing.T) {
	cases := []struct {
		action AutomationTaskActionType
		want   string
		wantOK bool
	}{
		{ActionInstallPackage, "install", true},
		{ActionUpdatePackage, "upgrade", true},
		{ActionUpdateOrInstallPackage, "upgrade", true},
		{ActionRemovePackage, "uninstall", true},
		{ActionRunScript, "", false},
		{ActionCustomCommand, "", false},
	}

	for _, tc := range cases {
		got, ok := packageChangeActionForTask(AutomationTask{ActionType: tc.action})
		if ok != tc.wantOK || got != tc.want {
			t.Fatalf("action %s: got (%q,%v), want (%q,%v)", tc.action, got, ok, tc.want, tc.wantOK)
		}
	}
}
