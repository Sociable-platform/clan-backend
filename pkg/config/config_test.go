package config

import "testing"

func TestBool(t *testing.T) {
	const key = "CONFIG_TEST_BOOL"
	tests := []struct {
		name    string
		value   string
		set     bool
		def     bool
		want    bool
		wantErr bool
	}{
		{"unset uses the default false", "", false, false, false, false},
		{"unset uses the default true", "", false, true, true, false},
		{"empty uses the default", "", true, true, true, false},
		{"true", "true", true, false, true, false},
		{"upper case TRUE", "TRUE", true, false, true, false},
		{"1", "1", true, false, true, false},
		{"false overrides a true default", "false", true, true, false, false},
		{"0", "0", true, true, false, false},
		{"a typo is an error", "ture", true, false, false, true},
		{"yes is not accepted", "yes", true, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(key, tt.value)
			}
			got, err := Bool(key, tt.def)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Bool = %v, want %v", got, tt.want)
			}
		})
	}
}
