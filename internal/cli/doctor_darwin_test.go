//go:build darwin

package cli

import "testing"

func TestParseGatewayLine(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    string
		wantErr bool
	}{
		{
			name: "typical route output",
			output: `   route to: default
destination: default
       mask: default
    gateway: 10.0.2.1
  interface: en0
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING>
`,
			want: "10.0.2.1",
		},
		{
			name: "extra whitespace",
			output: `gateway:    192.168.1.1
`,
			want: "192.168.1.1",
		},
		{
			name:    "missing gateway line",
			output:  "route to: default\ndestination: default\n",
			wantErr: true,
		},
		{
			name:    "empty gateway value",
			output:  "gateway: \n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGatewayLine(tt.output)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseGatewayLine() expected error, got nil (result %q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGatewayLine() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseGatewayLine() = %q, want %q", got, tt.want)
			}
		})
	}
}
