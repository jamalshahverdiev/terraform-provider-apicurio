package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Sentinel errors. Match them with errors.Is; the concrete value is always
// an *APIError (possibly wrapped in a typed error) with the server details.
var (
	// ErrNotFound: the object does not exist (a 404 whose body names the
	// missing object kind, e.g. GroupNotFoundException). A 404 without such a
	// body is NOT ErrNotFound: it means the URL is wrong.
	ErrNotFound = errors.New("not found")
	// ErrAlreadyExists: a 409 *AlreadyExistsException.
	ErrAlreadyExists = errors.New("already exists")
	// ErrUnauthorized: 401, no or rejected credentials.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrForbidden: 403, the credentials (or their absence) do not grant the
	// role the operation needs, or the server refuses the feature
	// ("Application RBAC not enabled.").
	ErrForbidden = errors.New("forbidden")
)

// ProblemDetails is the error body of the Apicurio Registry v3 API.
type ProblemDetails struct {
	Status int                  `json:"status"`
	Title  string               `json:"title"`
	Detail string               `json:"detail"`
	Name   string               `json:"name"`
	Causes []RuleViolationCause `json:"causes"`
}

// RuleViolationCause is one cause of a rule violation.
type RuleViolationCause struct {
	Description string `json:"description"`
	Context     string `json:"context"`
}

// APIError is a non-2xx answer of the registry.
type APIError struct {
	Operation   string // what was attempted, e.g. `read group "payments"`
	Method      string
	URL         string
	Status      int
	Name        string // the server exception class, e.g. ArtifactNotFoundException
	Title       string
	Detail      string
	Body        string // the raw body when it was not a ProblemDetails document
	Credentials bool   // whether the request carried credentials
}

// Error renders the status, the server message and a hint for 401/403 and
// for a 404 that is not an object lookup.
func (e *APIError) Error() string {
	var b strings.Builder
	if e.Operation != "" {
		b.WriteString(e.Operation)
		b.WriteString(": ")
	}
	fmt.Fprintf(&b, "%s %s returned HTTP %d", e.Method, e.URL, e.Status)
	switch {
	case e.Title != "":
		b.WriteString(": ")
		b.WriteString(e.Title)
	case e.Body != "":
		b.WriteString(": ")
		b.WriteString(e.Body)
	}
	switch e.Status {
	case http.StatusUnauthorized:
		if e.Credentials {
			b.WriteString(" (the configured credentials were rejected)")
		} else {
			b.WriteString(" (the request carried no credentials; configure token or username/password)")
		}
	case http.StatusForbidden:
		switch {
		case strings.Contains(e.Title, "RBAC not enabled"):
			// The server explains itself; a role hint would be wrong.
		case e.Credentials:
			b.WriteString(" (the configured credentials lack the role this operation needs)")
		default:
			b.WriteString(" (the request carried no credentials and this operation needs a role; configure token or username/password)")
		}
	case http.StatusNotFound:
		if !e.isObjectNotFound() {
			b.WriteString(" (no Apicurio Registry v3 endpoint answered here; check the provider url)")
		}
	}
	return b.String()
}

// Is lets errors.Is match the sentinel errors.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.isObjectNotFound()
	case ErrAlreadyExists:
		return e.Status == http.StatusConflict && strings.HasSuffix(e.Name, "AlreadyExistsException")
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrForbidden:
		return e.Status == http.StatusForbidden
	}
	return false
}

// isObjectNotFound: a 404 whose name is a specific *NotFoundException
// (GroupNotFoundException, ArtifactNotFoundException, ...). The bare
// NotFoundException is RESTEasy's answer for an unknown route or an invalid
// enum in the path.
func (e *APIError) isObjectNotFound() bool {
	return e.Status == http.StatusNotFound &&
		e.Name != "NotFoundException" &&
		strings.HasSuffix(e.Name, "NotFoundException")
}

// OperationDisabledError is the 405 NotAllowedException the registry answers
// when an operation is switched off by a dynamic property: the three
// deletions and the content update of draft versions (all off by default).
type OperationDisabledError struct {
	Err       *APIError
	Operation string // e.g. "group deletion", "artifact version content update"
	Property  string // the dynamic property that enables it
}

// Error names the property to turn on and the resource that can do it.
func (e *OperationDisabledError) Error() string {
	return fmt.Sprintf("%s: %s is disabled on this registry (HTTP 405). Set the dynamic property %s=true, "+
		"for example with an apicurio_config_property resource or the environment variable %s=true.",
		e.Err.Operation, e.Operation, e.Property, envName(e.Property))
}

// Unwrap exposes the underlying APIError.
func (e *OperationDisabledError) Unwrap() error { return e.Err }

// RuleViolationError is returned when content is refused by the configured
// rules (RuleViolationException: HTTP 400 on 3.3, 409 before) or cannot be
// parsed while a rule runs (UnprocessableSchemaException, HTTP 422).
type RuleViolationError struct {
	Err    *APIError
	Causes []RuleViolationCause
}

// Error renders the server title and every cause with its context.
func (e *RuleViolationError) Error() string {
	var b strings.Builder
	if e.Err.Operation != "" {
		b.WriteString(e.Err.Operation)
		b.WriteString(": ")
	}
	b.WriteString("the registry refused the content")
	if e.Err.Title != "" {
		b.WriteString(": ")
		b.WriteString(e.Err.Title)
	}
	for _, c := range e.Causes {
		b.WriteString("\n  - ")
		b.WriteString(c.Description)
		if c.Context != "" {
			b.WriteString(" at ")
			b.WriteString(c.Context)
		}
	}
	return b.String()
}

// Unwrap exposes the underlying APIError.
func (e *RuleViolationError) Unwrap() error { return e.Err }

// IsNotFound reports whether err means the object does not exist.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// decodeResponse maps a non-2xx status to an error and decodes a JSON 2xx
// body into out (when out is not nil and the status is not 204). It reads the
// body to its end so the connection can be reused; the caller closes it.
func (c *Client) decodeResponse(resp *http.Response, out any, op string) error {
	defer discardRest(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.errorFromResponse(resp, op)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decode the %d response of %s %s: %w", op, resp.StatusCode, reqMethod(resp), reqURL(resp), err)
	}
	return nil
}

func (c *Client) errorFromResponse(resp *http.Response, op string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ae := &APIError{
		Operation:   op,
		Method:      reqMethod(resp),
		URL:         reqURL(resp),
		Status:      resp.StatusCode,
		Credentials: c.HasCredentials(),
	}
	var pd ProblemDetails
	if json.Unmarshal(body, &pd) == nil && (pd.Name != "" || pd.Title != "") {
		ae.Name, ae.Title, ae.Detail = pd.Name, pd.Title, pd.Detail
	} else {
		ae.Body = truncate(strings.TrimSpace(string(body)), 500)
	}
	switch {
	case ae.Name == "RuleViolationException" || ae.Name == "UnprocessableSchemaException":
		return &RuleViolationError{Err: ae, Causes: pd.Causes}
	case ae.Status == http.StatusMethodNotAllowed && ae.Name == "NotAllowedException":
		if operation, prop, ok := disabledOperation(ae.Title); ok {
			return &OperationDisabledError{Err: ae, Operation: operation, Property: prop}
		}
	}
	return ae
}

// disabledOperation recognises the 405 titles of the switchable operations.
func disabledOperation(title string) (operation, property string, ok bool) {
	switch {
	case strings.HasPrefix(title, "Artifact version content update"):
		return "artifact version content update", PropertyContentMutability, true
	case strings.HasPrefix(title, "Artifact version deletion"):
		return "artifact version deletion", PropertyVersionDeletion, true
	case strings.HasPrefix(title, "Artifact deletion"):
		return "artifact deletion", PropertyArtifactDeletion, true
	case strings.HasPrefix(title, "Group deletion"):
		return "group deletion", PropertyGroupDeletion, true
	}
	return "", "", false
}

// envName converts a property name to the environment variable form the
// registry accepts (upper case, non-alphanumerics replaced by underscores).
func envName(property string) string {
	var b strings.Builder
	for _, r := range property {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// discardRest reads whatever is left of a body without closing it.
func discardRest(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func reqMethod(resp *http.Response) string {
	if resp.Request == nil {
		return "?"
	}
	return resp.Request.Method
}

func reqURL(resp *http.Response) string {
	if resp.Request == nil || resp.Request.URL == nil {
		return "?"
	}
	return resp.Request.URL.String()
}
