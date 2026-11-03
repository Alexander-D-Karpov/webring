package public

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSubmissionRejectsInvalidSiteBeforeDatabaseWrites(t *testing.T) {
	t.Setenv("REQUIRE_LOGIN_FOR_SUBMIT", "false")
	form := url.Values{"slug": {"sample"}, "name": {"Sample"}, "url": {"https://singlelabel"}}
	req := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	submitSiteHandler(nil).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid submission returned %d: %s", w.Code, w.Body.String())
	}
}
