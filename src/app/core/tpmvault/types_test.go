package tpmvault

import (
	"errors"
	"strings"
	"testing"
)

func TestFormatValue(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		host     string
		insecure bool
		want     string
		wantErr  bool
	}{
		{name: "https implicito", token: "mdz_abcdef123456", host: "api.example.com", want: "mdz_abcdef123456@api.example.com"},
		{name: "http explicito", token: "mdz_abcdef123456", host: "api.example.com", insecure: true, want: "mdz_abcdef123456@http://api.example.com"},
		{name: "barra final removida", token: "mdz_abcdef123456", host: "api.example.com/", want: "mdz_abcdef123456@api.example.com"},
		{name: "espacos removidos", token: "  mdz_abcdef123456  ", host: "  api.example.com  ", want: "mdz_abcdef123456@api.example.com"},
		{name: "token sem prefixo", token: "abcdef123456", host: "api.example.com", wantErr: true},
		{name: "token truncado", token: "mdz_", host: "api.example.com", wantErr: true},
		{name: "token vazio", token: "", host: "api.example.com", wantErr: true},
		{name: "token com arroba", token: "mdz_ab@c", host: "api.example.com", wantErr: true},
		{name: "token com espaco interno", token: "mdz_ab c", host: "api.example.com", wantErr: true},
		{name: "host vazio", token: "mdz_abcdef123456", host: "", wantErr: true},
		{name: "host com scheme", token: "mdz_abcdef123456", host: "https://api.example.com", wantErr: true},
		{name: "host com espaco", token: "mdz_abcdef123456", host: "api example.com", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FormatValue(tc.token, tc.host, tc.insecure)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("FormatValue() = %q, quer erro", got)
				}
				if !errors.Is(err, ErrInvalidValue) {
					t.Fatalf("erro = %v, quer ErrInvalidValue", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("FormatValue() erro inesperado: %v", err)
			}
			if got != tc.want {
				t.Fatalf("FormatValue() = %q, quer %q", got, tc.want)
			}
		})
	}
}

func TestParseValue(t *testing.T) {
	tests := []struct {
		name         string
		value        string
		wantToken    string
		wantHost     string
		wantInsecure bool
		wantErr      bool
	}{
		{name: "https implicito", value: "mdz_abcdef123456@api.example.com", wantToken: "mdz_abcdef123456", wantHost: "api.example.com"},
		{name: "http explicito", value: "mdz_abcdef123456@http://api.example.com", wantToken: "mdz_abcdef123456", wantHost: "api.example.com", wantInsecure: true},
		{name: "https explicito", value: "mdz_abcdef123456@https://api.example.com", wantToken: "mdz_abcdef123456", wantHost: "api.example.com"},
		{name: "barra final", value: "mdz_abcdef123456@api.example.com/", wantToken: "mdz_abcdef123456", wantHost: "api.example.com"},
		{name: "sem arroba", value: "mdz_abcdef123456", wantErr: true},
		{name: "arroba inicial", value: "@api.example.com", wantErr: true},
		{name: "arroba final", value: "mdz_abcdef123456@", wantErr: true},
		{name: "dois arrobas", value: "mdz_abcdef123456@a@b", wantErr: true},
		{name: "token sem prefixo", value: "abcdef@api.example.com", wantErr: true},
		{name: "host vazio", value: "mdz_abcdef123456@", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			token, host, insecure, err := ParseValue(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseValue(%q) = (%q,%q,%v), quer erro", tc.value, token, host, insecure)
				}
				if !errors.Is(err, ErrInvalidValue) {
					t.Fatalf("erro = %v, quer ErrInvalidValue", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseValue() erro inesperado: %v", err)
			}
			if token != tc.wantToken || host != tc.wantHost || insecure != tc.wantInsecure {
				t.Fatalf("ParseValue() = (%q,%q,%v), quer (%q,%q,%v)",
					token, host, insecure, tc.wantToken, tc.wantHost, tc.wantInsecure)
			}
		})
	}
}

func TestFormatParseRoundTrip(t *testing.T) {
	for _, insecure := range []bool{false, true} {
		value, err := FormatValue("mdz_roundtrip00001", "api.example.com", insecure)
		if err != nil {
			t.Fatalf("FormatValue: %v", err)
		}
		token, host, gotInsecure, err := ParseValue(value)
		if err != nil {
			t.Fatalf("ParseValue(%q): %v", value, err)
		}
		if token != "mdz_roundtrip00001" || host != "api.example.com" || gotInsecure != insecure {
			t.Fatalf("round-trip divergente: token=%q host=%q insecure=%v", token, host, gotInsecure)
		}
	}
}

func TestRecordMarshalParse(t *testing.T) {
	rec := Record{Schema: Schema, APIURL: "mdz_abcdef123456@api.example.com"}
	data, err := rec.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"API|URL"`) {
		t.Fatalf("Marshal não usou a chave API|URL: %s", data)
	}
	got, err := ParseRecord(data)
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if got != rec {
		t.Fatalf("ParseRecord = %+v, quer %+v", got, rec)
	}
	if got.Token() != "mdz_abcdef123456" || got.Host() != "api.example.com" || got.Insecure() {
		t.Fatalf("acessores divergentes: token=%q host=%q insecure=%v", got.Token(), got.Host(), got.Insecure())
	}
}

func TestRecordAllowInsecureTLS(t *testing.T) {
	rec := Record{Schema: Schema, APIURL: "mdz_abcdef123456@http://api.example.com", AllowInsecureTLS: true}
	data, err := rec.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := ParseRecord(data)
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if !got.AllowInsecureTLS || !got.Insecure() {
		t.Fatalf("AllowInsecureTLS perdido: %+v", got)
	}
}

func TestRecordRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		rec  Record
	}{
		{name: "schema errado", rec: Record{Schema: "outro.schema/v9", APIURL: "mdz_abcdef123456@api.example.com"}},
		{name: "schema vazio", rec: Record{APIURL: "mdz_abcdef123456@api.example.com"}},
		{name: "APIURL invalida", rec: Record{Schema: Schema, APIURL: "sem-arroba"}},
		{name: "token invalido", rec: Record{Schema: Schema, APIURL: "abcdef@api.example.com"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.rec.Marshal(); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("Marshal erro = %v, quer ErrInvalidValue", err)
			}
		})
	}

	if _, err := ParseRecord([]byte("{isso nao e json")); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("ParseRecord(json invalido) erro = %v, quer ErrInvalidValue", err)
	}
	if _, err := ParseRecord([]byte(`{"schema":"outro","API|URL":"mdz_abcdef123456@api.example.com"}`)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("ParseRecord(schema errado) erro = %v, quer ErrInvalidValue", err)
	}
}

func TestTokenPrefix(t *testing.T) {
	tests := []struct {
		token string
		want  string
	}{
		{token: "", want: "mdz_..."},
		{token: "abc", want: "mdz_..."},
		{token: "mdz_", want: "mdz_..."},
		{token: "mdz_abcdefg", want: "mdz_..."},
		{token: "mdz_abcdef1234567", want: "mdz_abcdef12..."},
	}
	for _, tc := range tests {
		if got := TokenPrefix(tc.token); got != tc.want {
			t.Fatalf("TokenPrefix(%q) = %q, quer %q", tc.token, got, tc.want)
		}
	}
	if got := TokenPrefix("mdz_supersecretotoken"); strings.Contains(got, "supersecretotoken") {
		t.Fatalf("TokenPrefix() expôs o token: %q", got)
	}
}
