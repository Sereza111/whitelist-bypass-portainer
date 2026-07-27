package wbstream

import (
	"strings"
	"testing"
)

func TestAccessTokenFromSlideBodyVariants(t *testing.T) {
	for _, body := range []string{
		`{"payload":{"access_token":"secret-a"}}`,
		`{"payload":{"accessToken":"secret-b"}}`,
		`{"access_token":"secret-c"}`,
		`{"accessToken":"secret-d"}`,
	} {
		token, schema := accessTokenFromSlideBody([]byte(body))
		if token == "" || strings.Contains(schema, "secret") {
			t.Fatalf("token variant failed or leaked through schema: %q", schema)
		}
	}
	if token, schema := accessTokenFromSlideBody([]byte(`{"error":"private detail","result":1}`)); token != "" || schema != "top=error,result payload=" {
		t.Fatalf("unexpected error schema token=%q schema=%q", token, schema)
	}
}
