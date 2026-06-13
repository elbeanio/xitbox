package cmd

import (
	"testing"
)

func TestExecuteDispatch(t *testing.T) {
	tests := []struct {
		args    []string
		wantErr bool
		errMsg  string
	}{
		// Management flags with no sub-flags should error
		{[]string{"--allow"}, true, "specify --domain, --cidr, or --from-log"},
		// Sub-flags without parent should error
		{[]string{"--domain", "foo.com"}, true, "--domain, --cidr, and --from-log require --allow"},
		{[]string{"--since", "5m"}, true, "--since and --follow require --logs"},
		{[]string{"--follow"}, true, "--since and --follow require --logs"},
		// Unknown flag should error
		{[]string{"--bogus"}, true, ""},
		// No args prints help (no error)
		{[]string{}, false, ""},
	}

	for _, tt := range tests {
		name := "no-args"
		if len(tt.args) > 0 {
			name = tt.args[0]
		}
		t.Run(name, func(t *testing.T) {
			err := execute(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("execute(%v) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
			if tt.errMsg != "" && err != nil {
				if err.Error() != tt.errMsg {
					t.Errorf("execute(%v) error = %q, want %q", tt.args, err.Error(), tt.errMsg)
				}
			}
		})
	}
}

