package issueops

import (
	"errors"
	"testing"
)

func TestIsTableNotExistError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("connection refused"),
			want: false,
		},
		{
			name: "doesn't exist lowercase",
			err:  errors.New("table 'foo' doesn't exist"),
			want: true,
		},
		{
			name: "doesn't exist mixed case",
			err:  errors.New("Table 'foo' Doesn't Exist"),
			want: true,
		},
		{
			name: "does not exist lowercase",
			err:  errors.New("table 'bar' does not exist"),
			want: true,
		},
		{
			name: "does not exist mixed case",
			err:  errors.New("Table 'bar' Does Not Exist"),
			want: true,
		},
		{
			name: "error 1146",
			err:  errors.New("Error 1146 (42S02): Table 'db.tbl' doesn't exist"),
			want: true,
		},
		{
			name: "error 1146 alone",
			err:  errors.New("error 1146"),
			want: true,
		},
		{
			name: "different mysql error code",
			err:  errors.New("Error 1045: Access denied"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isTableNotExistError(tt.err)
			if got != tt.want {
				t.Errorf("isTableNotExistError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
