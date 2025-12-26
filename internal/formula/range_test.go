package formula

import (
	"testing"
)

func TestEvaluateExpr(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		vars    map[string]string
		want    int
		wantErr bool
	}{
		{
			name: "simple integer",
			expr: "10",
			want: 10,
		},
		{
			name: "addition",
			expr: "2+3",
			want: 5,
		},
		{
			name: "subtraction",
			expr: "10-3",
			want: 7,
		},
		{
			name: "multiplication",
			expr: "4*5",
			want: 20,
		},
		{
			name: "division",
			expr: "20/4",
			want: 5,
		},
		{
			name: "power",
			expr: "2^3",
			want: 8,
		},
		{
			name: "power of 2",
			expr: "2^10",
			want: 1024,
		},
		{
			name: "complex expression",
			expr: "2+3*4",
			want: 14, // 2+(3*4) = 14, not (2+3)*4 = 20
		},
		{
			name: "parentheses",
			expr: "(2+3)*4",
			want: 20,
		},
		{
			name: "nested parentheses",
			expr: "((2+3)*(4+1))",
			want: 25,
		},
		{
			name: "variable substitution",
			expr: "{n}",
			vars: map[string]string{"n": "5"},
			want: 5,
		},
		{
			name: "power with variable",
			expr: "2^{n}",
			vars: map[string]string{"n": "4"},
			want: 16,
		},
		{
			name: "multiple variables",
			expr: "{a}+{b}",
			vars: map[string]string{"a": "10", "b": "20"},
			want: 30,
		},
		{
			name: "towers of hanoi pattern",
			expr: "2^{disks}-1",
			vars: map[string]string{"disks": "3"},
			want: 7, // 2^3-1 = 7
		},
		{
			name: "negative result",
			expr: "1-10",
			want: -9,
		},
		{
			name: "unary minus in expression",
			expr: "3*-2",
			want: -6,
		},
		{
			name: "parenthesized negative",
			expr: "(-5)",
			want: -5,
		},
		{
			name: "unary minus after power",
			expr: "2^-1",
			want: 0, // 0.5 truncated to int
		},
		{
			name: "division by zero",
			expr: "10/0",
			wantErr: true,
		},
		{
			name: "invalid expression",
			expr: "2++3",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EvaluateExpr(tt.expr, tt.vars)
			if (err != nil) != tt.wantErr {
				t.Errorf("EvaluateExpr(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("EvaluateExpr(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestParseRange(t *testing.T) {
	tests := []struct {
		name      string
		expr      string
		vars      map[string]string
		wantStart int
		wantEnd   int
		wantErr   bool
	}{
		{
			name:      "simple range",
			expr:      "1..10",
			wantStart: 1,
			wantEnd:   10,
		},
		{
			name:      "single value range",
			expr:      "5..5",
			wantStart: 5,
			wantEnd:   5,
		},
		{
			name:      "computed end",
			expr:      "1..2^3",
			wantStart: 1,
			wantEnd:   8,
		},
		{
			name:      "computed start and end",
			expr:      "2*2..3*3",
			wantStart: 4,
			wantEnd:   9,
		},
		{
			name:      "variables in range",
			expr:      "1..{n}",
			vars:      map[string]string{"n": "10"},
			wantStart: 1,
			wantEnd:   10,
		},
		{
			name:      "towers of hanoi moves",
			expr:      "1..2^{disks}-1",
			vars:      map[string]string{"disks": "3"},
			wantStart: 1,
			wantEnd:   7,
		},
		{
			name:      "both variables",
			expr:      "{start}..{end}",
			vars:      map[string]string{"start": "5", "end": "15"},
			wantStart: 5,
			wantEnd:   15,
		},
		{
			name:    "empty expression",
			expr:    "",
			wantErr: true,
		},
		{
			name:    "missing separator",
			expr:    "1 10",
			wantErr: true,
		},
		{
			name:    "invalid start expression",
			expr:    "abc..10",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRange(tt.expr, tt.vars)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseRange(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Start != tt.wantStart {
					t.Errorf("ParseRange(%q).Start = %v, want %v", tt.expr, got.Start, tt.wantStart)
				}
				if got.End != tt.wantEnd {
					t.Errorf("ParseRange(%q).End = %v, want %v", tt.expr, got.End, tt.wantEnd)
				}
			}
		})
	}
}

func TestValidateRange(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{
			name:    "valid simple range",
			expr:    "1..10",
			wantErr: false,
		},
		{
			name:    "valid computed range",
			expr:    "1..2^{n}",
			wantErr: false,
		},
		{
			name:    "valid complex range",
			expr:    "{start}..{end}*2",
			wantErr: false,
		},
		{
			name:    "empty",
			expr:    "",
			wantErr: true,
		},
		{
			name:    "no separator",
			expr:    "10",
			wantErr: true,
		},
		{
			name:    "invalid character",
			expr:    "1..@10",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRange(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRange(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
			}
		})
	}
}
