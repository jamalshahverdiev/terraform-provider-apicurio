package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jarcoal/httpmock"
	"gopkg.in/yaml.v3"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

// fakeRegistry is an in-memory Apicurio Registry 3.3 that serves the
// endpoints the provider uses, with the response shapes, error names and
// behaviours recorded in docker-compose/LIVE-SERVER-FINDINGS.md: deletions
// switched off answer 405, a missing group with automatic creation off is a
// 404 GroupNotFoundException, rules refuse content with a 400
// RuleViolationException and its causes, metadata updates change only the
// properties present, an artifact survives the deletion of its last version.
type fakeRegistry struct {
	mu            sync.Mutex
	groups        map[string]*fakeGroup
	artifacts     map[string]*fakeArtifact // group + "\x00" + artifact
	rules         map[string]map[string]string
	roles         map[string]*client.RoleMapping // role mappings by principal id
	lastAuth      string                         // Authorization header of the last request
	props         map[string]string
	autoGroup     bool
	nextGlobalID  int64
	nextContentID int64
	contentIDs    map[string]int64
	contentTypes  map[string]string // first content type seen for a content hash, like the registry
}

type fakeGroup struct {
	ID, Description, Owner, CreatedOn, ModifiedOn, ModifiedBy string
	Labels                                                    map[string]string
}

type fakeArtifact struct {
	GroupID, ID, Type, Name, Description, Owner, CreatedOn, ModifiedOn, ModifiedBy string
	Labels                                                                         map[string]string
	versions                                                                       []*fakeVersion
	branches                                                                       map[string]*fakeBranch
	latest                                                                         []string // versions in the order they joined the latest branch
	draftsBranch, latestBranch                                                     bool     // system branches exist once a draft / a non-draft version did
}

type fakeBranch struct {
	ID, Description, CreatedOn, ModifiedOn string
	Versions                               []string // insertion order (oldest first)
}

type fakeVersion struct {
	Version             string
	GlobalID, ContentID int64
	State, ContentType  string
	Content             []byte
	References          []client.Reference
	Name, Description   string
	Labels              map[string]string
	CreatedOn           string
	ModifiedOn          string
}

var fakeArtifactTypes = []string{"PROTOBUF", "OPENAPI", "ASYNCAPI", "JSON", "AVRO", "GRAPHQL", "KCONNECT", "WSDL", "XSD", "XML", "AGENT_CARD", "MCP_TOOL"}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		groups:       map[string]*fakeGroup{},
		artifacts:    map[string]*fakeArtifact{},
		rules:        map[string]map[string]string{},
		props:        defaultFakeProps(),
		roles:        map[string]*client.RoleMapping{},
		autoGroup:    true,
		contentIDs:   map[string]int64{},
		contentTypes: map[string]string{},
	}
}

// install routes every request of the default transport to the fake.
func (f *fakeRegistry) install() {
	httpmock.RegisterNoResponder(f.handle)
}

func fakeNow() string { return time.Now().UTC().Format(time.RFC3339) }

func jsonResponse(status int, v any) (*http.Response, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	resp := httpmock.NewBytesResponse(status, b)
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

func problemResponse(status int, name, title string, causes ...client.RuleViolationCause) (*http.Response, error) {
	body := map[string]any{"status": status, "name": name, "title": title, "detail": name + ": " + title}
	if name == "RuleViolationException" {
		if causes == nil {
			causes = []client.RuleViolationCause{}
		}
		body["causes"] = causes
	}
	return jsonResponse(status, body)
}

func noContent() (*http.Response, error) { return httpmock.NewStringResponse(204, ""), nil }

func (f *fakeRegistry) handle(req *http.Request) (*http.Response, error) {
	resp, err := f.dispatch(req)
	if resp != nil {
		resp.Request = req // like a real server answer, so error messages show method and URL
	}
	return resp, err
}

func (f *fakeRegistry) dispatch(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastAuth = req.Header.Get("Authorization")
	// the escaped path keeps a %2F inside an artifact id out of the split
	ep := req.URL.EscapedPath()
	p := strings.TrimPrefix(ep, client.APIPath)
	if p == ep {
		return problemResponse(404, "NotFoundException", "RESTEASY003210: Could not find resource for full path: "+req.URL.String())
	}
	raw := strings.Split(strings.Trim(p, "/"), "/")
	seg := make([]string, 0, len(raw))
	for _, s := range raw {
		u, err := url.PathUnescape(s)
		if err != nil {
			u = s
		}
		seg = append(seg, u)
	}
	var body map[string]any
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		if len(b) > 0 {
			if err := json.Unmarshal(b, &body); err != nil {
				return problemResponse(400, "BadRequestException", "Not able to deserialize data provided.")
			}
		}
	}
	q := req.URL.Query()
	m := req.Method

	switch {
	case len(seg) == 2 && seg[0] == "system" && seg[1] == "info" && m == "GET":
		return jsonResponse(200, map[string]any{"name": "Apicurio Registry (SQL)", "description": "fake", "version": mockVersion, "builtOn": "2026-11-03T19:25:03Z"})
	case len(seg) == 2 && seg[0] == "users" && seg[1] == "me" && m == "GET":
		return jsonResponse(200, map[string]any{"username": "", "displayName": "", "admin": true, "developer": false, "viewer": false})
	case len(seg) == 3 && seg[0] == "admin" && seg[1] == "config" && seg[2] == "artifactTypes" && m == "GET":
		out := make([]map[string]string, 0, len(fakeArtifactTypes))
		for _, t := range fakeArtifactTypes {
			out = append(out, map[string]string{"name": t})
		}
		return jsonResponse(200, out)
	case len(seg) >= 3 && seg[0] == "admin" && seg[1] == "config" && seg[2] == "properties":
		return f.handleProperties(m, seg[3:], body)
	case len(seg) >= 2 && seg[0] == "admin" && seg[1] == "roleMappings":
		return f.handleRoleMappings(m, seg[2:], q, body)
	case len(seg) == 2 && seg[0] == "search" && seg[1] == "artifacts" && m == "GET":
		return f.handleSearchArtifacts(q)
	case len(seg) >= 2 && seg[0] == "admin" && seg[1] == "rules":
		return f.handleRules(m, "", seg[2:], body)
	case seg[0] == "groups":
		return f.handleGroups(m, seg[1:], q, body)
	}
	return problemResponse(404, "NotFoundException", "RESTEASY003210: Could not find resource for full path: "+req.URL.String())
}

// ------------------------------------------------------------------ properties

// fakePropertyInfo describes a dynamic property like the registry's listing.
type fakePropertyInfo struct {
	Type, Default, Label string
	Allowed              []string // for the log level
}

// fakeProperties are the dynamic properties of the fake (a subset of the
// registry's, with the compose defaults) plus a fake-only switch for RBAC.
var fakeProperties = map[string]fakePropertyInfo{
	client.PropertyGroupDeletion:         {Type: "java.lang.Boolean", Default: "true", Label: "Delete group"},
	client.PropertyArtifactDeletion:      {Type: "java.lang.Boolean", Default: "true", Label: "Delete artifact"},
	client.PropertyVersionDeletion:       {Type: "java.lang.Boolean", Default: "true", Label: "Delete artifact version"},
	client.PropertyContentMutability:     {Type: "java.lang.Boolean", Default: "true", Label: "Update artifact version content"},
	"apicurio.semver.validation.enabled": {Type: "java.lang.Boolean", Default: "false", Label: "Ensure all version numbers are 'semver' compatible"},
	"apicurio.storage.read-only.enabled": {Type: "java.lang.Boolean", Default: "false", Label: "Storage read-only mode"},
	"apicurio.ccompat.max-subjects":      {Type: "java.lang.Integer", Default: "1000", Label: "Maximum number of Subjects returned (compatibility API)"},
	"apicurio.download.href.ttl.seconds": {Type: "java.lang.Long", Default: "30", Label: "Download link expiry"},
	"apicurio.log.level":                 {Type: "java.lang.String", Default: "INFO", Label: "Log level", Allowed: []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR", "OFF", "ALL"}},
	fakeRoleSourceProperty:               {Type: "java.lang.String", Default: "application", Label: "Role source (fake only)", Allowed: []string{"application", "token"}},
}

func defaultFakeProps() map[string]string {
	out := map[string]string{}
	for name, info := range fakeProperties {
		out[name] = info.Default
	}
	return out
}

// fakePropertyValueOK mirrors the registry's type check (boolean, integer,
// long; the log level has its own list). Values are stored as written.
func fakePropertyValueOK(info fakePropertyInfo, v string) bool {
	switch {
	case v == "":
		return false
	case info.Allowed != nil:
		return slices.Contains(info.Allowed, v)
	case info.Type == "java.lang.Boolean":
		l := strings.ToLower(v)
		return l == "true" || l == "false"
	case info.Type == "java.lang.Integer":
		_, err := strconv.ParseInt(v, 10, 32)
		return err == nil
	case info.Type == "java.lang.Long":
		_, err := strconv.ParseInt(v, 10, 64)
		return err == nil
	}
	return true
}

func (f *fakeRegistry) handleProperties(m string, rest []string, body map[string]any) (*http.Response, error) {
	propOut := func(name string) map[string]any {
		info := fakeProperties[name]
		return map[string]any{"name": name, "value": f.props[name], "type": info.Type, "label": info.Label, "description": info.Label + "."}
	}
	if len(rest) == 0 {
		if m != "GET" {
			return problemResponse(405, "NotAllowedException", "method")
		}
		names := make([]string, 0, len(f.props))
		for n := range f.props {
			names = append(names, n)
		}
		sort.Strings(names)
		out := make([]map[string]any, 0, len(names))
		for _, n := range names {
			out = append(out, propOut(n))
		}
		return jsonResponse(200, out)
	}
	name := rest[0]
	if _, ok := f.props[name]; !ok {
		return problemResponse(404, "ConfigPropertyNotFoundException", fmt.Sprintf("No configuration property named '%s' was found.", name))
	}
	switch m {
	case "GET":
		return jsonResponse(200, propOut(name))
	case "PUT":
		v, _ := body["value"].(string)
		info := fakeProperties[name]
		if !fakePropertyValueOK(info, v) {
			if info.Allowed != nil && name == "apicurio.log.level" {
				return problemResponse(400, "InvalidPropertyValueException", "Invalid log level value: "+v+". Must be one of: TRACE, DEBUG, INFO, WARN, ERROR, OFF, ALL")
			}
			return problemResponse(400, "InvalidPropertyValueException", "Invalid dynamic configuration property value for: "+name)
		}
		f.props[name] = v
		return noContent()
	case "DELETE":
		f.props[name] = fakeProperties[name].Default // the configured default
		return noContent()
	}
	return problemResponse(405, "NotAllowedException", "method")
}

// ------------------------------------------------------------------ rules

func (f *fakeRegistry) handleRules(m, scope string, rest []string, body map[string]any) (*http.Response, error) {
	rules := f.rules[scope]
	if rules == nil {
		rules = map[string]string{}
		f.rules[scope] = rules
	}
	if len(rest) == 0 {
		switch m {
		case "GET":
			types := make([]string, 0, len(rules))
			for t := range rules {
				types = append(types, t)
			}
			sort.Strings(types)
			return jsonResponse(200, types)
		case "POST":
			t, _ := body["ruleType"].(string)
			cfg, _ := body["config"].(string)
			if t != client.RuleValidity && t != client.RuleCompatibility && t != client.RuleIntegrity {
				return problemResponse(400, "BadRequestException", "Not able to deserialize data provided.")
			}
			if cfg == "" {
				return problemResponse(400, "MissingRequiredParameterException", "Request is missing a required parameter: config")
			}
			if _, ok := rules[t]; ok {
				return problemResponse(409, "RuleAlreadyExistsException", fmt.Sprintf("A rule named '%s' already exists.", t))
			}
			rules[t] = cfg
			return noContent()
		case "DELETE":
			clear(rules)
			return noContent()
		}
		return problemResponse(405, "NotAllowedException", "method")
	}
	t := rest[0]
	cfg, ok := rules[t]
	switch m {
	case "GET":
		if !ok {
			return problemResponse(404, "RuleNotFoundException", fmt.Sprintf("No rule named '%s' was found.", t))
		}
		return jsonResponse(200, map[string]string{"ruleType": t, "config": cfg})
	case "PUT":
		if !ok {
			return problemResponse(404, "RuleNotFoundException", fmt.Sprintf("No rule named '%s' was found.", t))
		}
		newCfg, _ := body["config"].(string)
		rules[t] = newCfg
		return jsonResponse(200, map[string]string{"ruleType": t, "config": newCfg})
	case "DELETE":
		if !ok {
			return problemResponse(404, "RuleNotFoundException", fmt.Sprintf("No rule named '%s' was found.", t))
		}
		delete(rules, t)
		return noContent()
	}
	return problemResponse(405, "NotAllowedException", "method")
}

// ------------------------------------------------------------------ search

// handleSearchArtifacts mirrors GET /search/artifacts of 3.1+: groupId,
// artifactId, artifactType and name match exactly, description is a
// substring, every labels= entry ("k" or "k:v") must match (split on the
// first colon like 3.0 to 3.2; 3.3 splits on the last one, which is why the
// provider refuses colons in the filter); the default order is by name with
// unnamed artifacts last.
func (f *fakeRegistry) handleSearchArtifacts(q url.Values) (*http.Response, error) {
	var hits []*fakeArtifact
	for _, a := range f.artifacts {
		if v := q.Get("groupId"); v != "" && a.GroupID != v {
			continue
		}
		if v := q.Get("artifactId"); v != "" && a.ID != v {
			continue
		}
		if v := q.Get("artifactType"); v != "" && a.Type != v {
			continue
		}
		if v := q.Get("name"); v != "" && a.Name != v {
			continue
		}
		if v := q.Get("description"); v != "" && !strings.Contains(a.Description, v) {
			continue
		}
		ok := true
		for _, l := range q["labels"] {
			key, val, hasVal := strings.Cut(l, ":")
			got, present := a.Labels[key]
			if !present || (hasVal && got != val) {
				ok = false
				break
			}
		}
		if ok {
			hits = append(hits, a)
		}
	}
	key := func(a *fakeArtifact) string {
		switch q.Get("orderby") {
		case "createdOn":
			return a.CreatedOn + "\x00" + a.ID
		case "modifiedOn":
			return a.ModifiedOn + "\x00" + a.ID
		case "groupId":
			return a.GroupID + "\x00" + a.ID
		case "artifactId":
			return a.ID
		case "artifactType":
			return a.Type + "\x00" + a.ID
		}
		if a.Name == "" {
			return "\xff" + a.ID // unnamed last
		}
		return a.Name + "\x00" + a.ID
	}
	sort.Slice(hits, func(i, j int) bool { return key(hits[i]) < key(hits[j]) })
	if q.Get("order") == "desc" {
		for i, j := 0, len(hits)-1; i < j; i, j = i+1, j-1 {
			hits[i], hits[j] = hits[j], hits[i]
		}
	}
	ids := make([]string, 0, len(hits))
	byKey := map[string]*fakeArtifact{}
	for _, a := range hits {
		k := a.GroupID + "\x00" + a.ID
		ids = append(ids, k)
		byKey[k] = a
	}
	items := []map[string]any{}
	for _, k := range page(ids, q) {
		items = append(items, f.artifactOut(byKey[k]))
	}
	return jsonResponse(200, map[string]any{"artifacts": items, "count": len(ids)})
}

// ------------------------------------------------------------------ role mappings

func (f *fakeRegistry) handleRoleMappings(m string, rest []string, q url.Values, body map[string]any) (*http.Response, error) {
	if f.props[fakeRoleSourceProperty] != "application" {
		return problemResponse(403, "ForbiddenException", "Application RBAC not enabled.")
	}
	validRole := func(r string) bool {
		return r == client.RoleReadOnly || r == client.RoleDeveloper || r == client.RoleAdmin
	}
	if len(rest) == 0 {
		switch m {
		case "GET":
			ids := make([]string, 0, len(f.roles))
			for id := range f.roles {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			items := []*client.RoleMapping{}
			for _, id := range page(ids, q) {
				items = append(items, f.roles[id])
			}
			return jsonResponse(200, map[string]any{"roleMappings": items, "count": len(ids)})
		case "POST":
			id := asString(body["principalId"])
			role := asString(body["role"])
			if !validRole(role) {
				return problemResponse(400, "BadRequestException", "Not able to deserialize data provided.")
			}
			if _, ok := f.roles[id]; ok {
				return problemResponse(409, "RoleMappingAlreadyExistsException", fmt.Sprintf("A mapping for principal '%s' and role '%s' already exists.", id, role))
			}
			f.roles[id] = &client.RoleMapping{PrincipalID: id, Role: role, PrincipalName: asString(body["principalName"])}
			return noContent()
		}
		return problemResponse(405, "NotAllowedException", "method")
	}
	id := rest[0]
	rm, ok := f.roles[id]
	switch m {
	case "GET":
		if !ok {
			return problemResponse(404, "RoleMappingNotFoundException", fmt.Sprintf("No role mapping for principal '%s' was found.", id))
		}
		return jsonResponse(200, rm)
	case "PUT":
		role := asString(body["role"])
		if !validRole(role) {
			return problemResponse(400, "BadRequestException", "Not able to deserialize data provided.")
		}
		if !ok {
			return problemResponse(404, "RoleMappingNotFoundException", fmt.Sprintf("No mapping for principal '%s' and role '%s' was found.", id, role))
		}
		rm.Role = role
		return noContent()
	case "DELETE":
		if !ok {
			return problemResponse(404, "RoleMappingNotFoundException", fmt.Sprintf("No role mapping for principal '%s' was found.", id))
		}
		delete(f.roles, id)
		return noContent()
	}
	return problemResponse(405, "NotAllowedException", "method")
}

// ------------------------------------------------------------------ groups

func (f *fakeRegistry) groupOut(g *fakeGroup) map[string]any {
	out := map[string]any{"groupId": g.ID, "owner": g.Owner, "createdOn": g.CreatedOn, "modifiedBy": g.ModifiedBy, "modifiedOn": g.ModifiedOn}
	if g.Description != "" {
		out["description"] = g.Description
	}
	if len(g.Labels) > 0 {
		out["labels"] = g.Labels
	}
	return out
}

func (f *fakeRegistry) handleGroups(m string, rest []string, q url.Values, body map[string]any) (*http.Response, error) {
	if len(rest) == 0 {
		switch m {
		case "GET":
			ids := make([]string, 0, len(f.groups))
			for id := range f.groups {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			items := []map[string]any{}
			for _, id := range page(ids, q) {
				items = append(items, f.groupOut(f.groups[id]))
			}
			return jsonResponse(200, map[string]any{"groups": items, "count": len(ids)})
		case "POST":
			id, _ := body["groupId"].(string)
			if id == "default" {
				return problemResponse(400, "BadRequestException", "The group name 'default' is reserved and cannot be used.")
			}
			if _, ok := f.groups[id]; ok {
				return problemResponse(409, "GroupAlreadyExistsException", fmt.Sprintf("Group '%s' already exists.", id))
			}
			g := &fakeGroup{ID: id, CreatedOn: fakeNow(), ModifiedOn: fakeNow()}
			g.Description, _ = body["description"].(string)
			g.Labels = labelsOf(body["labels"])
			f.groups[id] = g
			return jsonResponse(200, f.groupOut(g))
		}
		return problemResponse(405, "NotAllowedException", "method")
	}
	groupID := rest[0]
	rest = rest[1:]
	if len(rest) >= 1 && rest[0] == "rules" {
		if _, ok := f.groups[groupID]; !ok {
			return problemResponse(404, "GroupNotFoundException", fmt.Sprintf("No group '%s' was found.", groupID))
		}
		return f.handleRules(m, "g:"+groupID, rest[1:], body)
	}
	if len(rest) >= 1 && rest[0] == "artifacts" {
		return f.handleArtifacts(m, groupID, rest[1:], q, body)
	}
	if len(rest) != 0 {
		return problemResponse(404, "NotFoundException", "RESTEASY003210: Could not find resource for full path")
	}
	g, ok := f.groups[groupID]
	if !ok {
		return problemResponse(404, "GroupNotFoundException", fmt.Sprintf("No group '%s' was found.", groupID))
	}
	switch m {
	case "GET":
		return jsonResponse(200, f.groupOut(g))
	case "PUT":
		if v, ok := body["description"]; ok && v != nil {
			g.Description = v.(string)
		}
		if v, ok := body["labels"]; ok && v != nil {
			g.Labels = labelsOf(v)
		}
		if v, ok := body["owner"]; ok && v != nil {
			if v.(string) == "" {
				return problemResponse(400, "MissingRequiredParameterException", "Request is missing a required parameter: Owner cannot be empty")
			}
			g.Owner = v.(string)
		}
		g.ModifiedOn = fakeNow()
		return noContent()
	case "DELETE":
		if f.props[client.PropertyGroupDeletion] != "true" {
			return problemResponse(405, "NotAllowedException", "Group deletion operation is not enabled.")
		}
		delete(f.groups, groupID)
		delete(f.rules, "g:"+groupID)
		for k, a := range f.artifacts {
			if a.GroupID == groupID {
				delete(f.artifacts, k)
				delete(f.rules, "a:"+groupID+"/"+a.ID)
			}
		}
		return noContent()
	}
	return problemResponse(405, "NotAllowedException", "method")
}

// ------------------------------------------------------------------ artifacts

func (f *fakeRegistry) artifactOut(a *fakeArtifact) map[string]any {
	out := map[string]any{"groupId": a.GroupID, "artifactId": a.ID, "artifactType": a.Type, "owner": a.Owner,
		"createdOn": a.CreatedOn, "modifiedBy": a.ModifiedBy, "modifiedOn": a.ModifiedOn}
	if a.Name != "" {
		out["name"] = a.Name
	}
	if a.Description != "" {
		out["description"] = a.Description
	}
	if len(a.Labels) > 0 {
		out["labels"] = a.Labels
	}
	return out
}

func (f *fakeRegistry) versionOut(a *fakeArtifact, v *fakeVersion) map[string]any {
	out := map[string]any{"groupId": a.GroupID, "artifactId": a.ID, "version": v.Version, "globalId": v.GlobalID,
		"contentId": v.ContentID, "state": v.State, "artifactType": a.Type, "owner": a.Owner, "createdOn": v.CreatedOn,
		"modifiedBy": "", "modifiedOn": v.ModifiedOn}
	if v.Name != "" {
		out["name"] = v.Name
	}
	if v.Description != "" {
		out["description"] = v.Description
	}
	if len(v.Labels) > 0 {
		out["labels"] = v.Labels
	}
	return out
}

func (f *fakeRegistry) handleArtifacts(m, groupID string, rest []string, q url.Values, body map[string]any) (*http.Response, error) {
	if len(rest) == 0 {
		switch m {
		case "GET":
			if _, ok := f.groups[groupID]; !ok {
				return problemResponse(404, "GroupNotFoundException", fmt.Sprintf("No group '%s' was found.", groupID))
			}
			ids := []string{}
			for _, a := range f.artifacts {
				if a.GroupID == groupID {
					ids = append(ids, a.ID)
				}
			}
			sort.Strings(ids)
			items := []map[string]any{}
			for _, id := range page(ids, q) {
				items = append(items, f.artifactOut(f.artifacts[groupID+"\x00"+id]))
			}
			return jsonResponse(200, map[string]any{"artifacts": items, "count": len(ids)})
		case "POST":
			return f.createArtifact(groupID, q, body)
		}
		return problemResponse(405, "NotAllowedException", "method")
	}
	artifactID := rest[0]
	rest = rest[1:]
	a, ok := f.artifacts[groupID+"\x00"+artifactID]
	if !ok {
		return problemResponse(404, "ArtifactNotFoundException", fmt.Sprintf("No artifact with ID '%s' in group '%s' was found.", artifactID, groupID))
	}
	if len(rest) >= 1 && rest[0] == "rules" {
		return f.handleRules(m, "a:"+groupID+"/"+artifactID, rest[1:], body)
	}
	if len(rest) >= 1 && rest[0] == "versions" {
		return f.handleVersions(m, a, rest[1:], q, body)
	}
	if len(rest) >= 1 && rest[0] == "branches" {
		return f.handleBranches(m, a, rest[1:], q, body)
	}
	if len(rest) != 0 {
		return problemResponse(404, "NotFoundException", "RESTEASY003210: Could not find resource for full path")
	}
	switch m {
	case "GET":
		return jsonResponse(200, f.artifactOut(a))
	case "PUT":
		if v, ok := body["name"]; ok && v != nil {
			a.Name = v.(string)
		}
		if v, ok := body["description"]; ok && v != nil {
			a.Description = v.(string)
		}
		if v, ok := body["labels"]; ok && v != nil {
			a.Labels = labelsOf(v)
		}
		if v, ok := body["owner"]; ok && v != nil {
			if v.(string) == "" {
				return problemResponse(400, "MissingRequiredParameterException", "Request is missing a required parameter: Owner cannot be empty")
			}
			a.Owner = v.(string)
		}
		a.ModifiedOn = fakeNow()
		return noContent()
	case "DELETE":
		if f.props[client.PropertyArtifactDeletion] != "true" {
			return problemResponse(405, "NotAllowedException", "Artifact deletion operation is not enabled.")
		}
		delete(f.artifacts, groupID+"\x00"+artifactID)
		delete(f.rules, "a:"+groupID+"/"+artifactID)
		return noContent()
	}
	return problemResponse(405, "NotAllowedException", "method")
}

func (f *fakeRegistry) createArtifact(groupID string, q url.Values, body map[string]any) (*http.Response, error) {
	if _, ok := f.groups[groupID]; !ok {
		if !f.autoGroup {
			return problemResponse(404, "GroupNotFoundException", fmt.Sprintf("No group '%s' was found.", groupID))
		}
		f.groups[groupID] = &fakeGroup{ID: groupID, CreatedOn: fakeNow(), ModifiedOn: fakeNow()}
	}
	artifactID, _ := body["artifactId"].(string)
	artifactType, _ := body["artifactType"].(string)
	fv, _ := body["firstVersion"].(map[string]any)
	key := groupID + "\x00" + artifactID
	if existing, ok := f.artifacts[key]; ok {
		ifExists := q.Get("ifExists")
		if ifExists == "" || ifExists == client.IfExistsFail {
			return problemResponse(409, "ArtifactAlreadyExistsException", fmt.Sprintf("An artifact with ID '%s' in group '%s' already exists.", artifactID, groupID))
		}
		if fv == nil {
			return jsonResponse(200, map[string]any{"artifact": f.artifactOut(existing)})
		}
		v, resp, err := f.addVersion(existing, fv, false)
		if resp != nil || err != nil {
			return resp, err
		}
		return jsonResponse(200, map[string]any{"artifact": f.artifactOut(existing), "version": f.versionOut(existing, v)})
	}
	if artifactType != "" {
		known := false
		for _, t := range fakeArtifactTypes {
			if t == artifactType {
				known = true
			}
		}
		if !known {
			return problemResponse(500, "IllegalStateException", "No such artifact type provider: "+artifactType)
		}
	} else {
		if fv == nil {
			return problemResponse(400, "InvalidArtifactTypeException", "Failed to discover artifact type from content.")
		}
		content, _ := fv["content"].(map[string]any)
		c, _ := content["content"].(string)
		ct, _ := content["contentType"].(string)
		detected, ok := detectArtifactType(ct, c)
		if refs, _ := content["references"].([]any); len(refs) > 0 {
			ok = false // like the registry: content with external named types does not parse on its own
		}
		if !ok {
			return problemResponse(400, "InvalidArtifactTypeException", "Failed to discover artifact type from content.")
		}
		artifactType = detected
	}
	a := &fakeArtifact{GroupID: groupID, ID: artifactID, Type: artifactType, CreatedOn: fakeNow(), ModifiedOn: fakeNow()}
	a.Name, _ = body["name"].(string)
	a.Description, _ = body["description"].(string)
	a.Labels = labelsOf(body["labels"])
	out := map[string]any{"artifact": f.artifactOut(a)}
	if fv != nil {
		v, resp, err := f.addVersion(a, fv, true)
		if resp != nil || err != nil {
			return resp, err
		}
		out["version"] = f.versionOut(a, v)
	}
	if q.Get("dryRun") == "true" {
		return jsonResponse(200, out)
	}
	f.artifacts[key] = a
	return jsonResponse(200, out)
}

// addVersion applies the rules and stores a version. It returns a response
// when the version is refused.
func (f *fakeRegistry) addVersion(a *fakeArtifact, in map[string]any, first bool) (*fakeVersion, *http.Response, error) {
	content, _ := in["content"].(map[string]any)
	c, _ := content["content"].(string)
	ct, _ := content["contentType"].(string)
	refs := referencesOf(content["references"])
	versionID, _ := in["version"].(string)
	if versionID == "" {
		// like the registry: max existing numeric id + 1 (so ids are reused after deletions)
		next := 1
		for _, v := range a.versions {
			if n, err := strconv.Atoi(v.Version); err == nil && n >= next {
				next = n + 1
			}
		}
		versionID = strconv.Itoa(next)
	}
	for _, v := range a.versions {
		if v.Version == versionID {
			resp, err := problemResponse(409, "VersionAlreadyExistsException", fmt.Sprintf("An artifact version '%s' for artifact ID '%s' in group '%s' already exists.", versionID, a.ID, a.GroupID))
			return nil, resp, err
		}
	}
	isDraft, _ := in["isDraft"].(bool)
	if !isDraft {
		if resp, err := f.applyRules(a, c, refs, first); resp != nil || err != nil {
			return nil, resp, err
		}
	}
	f.nextGlobalID++
	state := "ENABLED"
	var cid int64
	if isDraft {
		// like 3.2+: a draft does not use the real content hash until promoted
		state = "DRAFT"
		f.nextContentID++
		cid = f.nextContentID
	} else {
		cid, ct = f.contentID(c, ct)
	}
	now := fakeNow()
	v := &fakeVersion{Version: versionID, GlobalID: f.nextGlobalID, ContentID: cid, State: state, ContentType: ct, Content: []byte(c), References: refs, CreatedOn: now, ModifiedOn: now}
	v.Name, _ = in["name"].(string)
	v.Description, _ = in["description"].(string)
	v.Labels = labelsOf(in["labels"])
	a.versions = append(a.versions, v)
	if isDraft {
		a.draftsBranch = true
	} else {
		a.latest = append(a.latest, v.Version)
		a.latestBranch = true
	}
	return v, nil, nil
}

// effectiveRule resolves a rule with the precedence artifact, group, global.
func (f *fakeRegistry) effectiveRule(a *fakeArtifact, ruleType string) string {
	for _, scope := range []string{"a:" + a.GroupID + "/" + a.ID, "g:" + a.GroupID, ""} {
		if cfg, ok := f.rules[scope][ruleType]; ok {
			return cfg
		}
	}
	return ""
}

func (f *fakeRegistry) applyRules(a *fakeArtifact, content string, refs []client.Reference, first bool) (*http.Response, error) {
	var prev *fakeVersion
	if !first {
		prev = f.ruleTarget(a)
	}
	return f.applyRulesAgainst(a, prev, content, refs)
}

// latestTip is the tip of the `latest` branch: the version that joined the
// branch last (a promoted draft joins at promotion) and is not disabled.
func (f *fakeRegistry) latestTip(a *fakeArtifact) *fakeVersion {
	for i := len(a.latest) - 1; i >= 0; i-- {
		if v := f.resolveVersion(a, a.latest[i]); v != nil && v.State != client.StateDisabled {
			return v
		}
	}
	return nil
}

// ruleTarget is the version the registry compares new content with: the
// newest version by creation that is not disabled, drafts included (so a
// draft promoted while it is the newest is compared with itself).
func (f *fakeRegistry) ruleTarget(a *fakeArtifact) *fakeVersion {
	for i := len(a.versions) - 1; i >= 0; i-- {
		if v := a.versions[i]; v.State != client.StateDisabled {
			return v
		}
	}
	return nil
}

func (f *fakeRegistry) applyRulesAgainst(a *fakeArtifact, prev *fakeVersion, content string, refs []client.Reference) (*http.Response, error) {
	if cfg := f.effectiveRule(a, client.RuleValidity); cfg != "" && cfg != "NONE" {
		switch {
		case jsonTypes[a.Type]:
			if !json.Valid([]byte(content)) || (a.Type == "AVRO" && !avroSchemaValid(content)) {
				return problemResponse(400, "RuleViolationException", "Syntax violation for Avro artifact.")
			}
		case a.Type == "PROTOBUF":
			if !protoSchemaValid(content) {
				return problemResponse(400, "RuleViolationException", "Syntax violation for Protobuf artifact.")
			}
		}
	}
	if cfg := f.effectiveRule(a, client.RuleIntegrity); cfg != "" && cfg != "NONE" && (strings.Contains(cfg, "REFS_EXIST") || strings.Contains(cfg, "FULL")) {
		for _, r := range refs {
			target, ok := f.artifacts[r.GroupID+"\x00"+r.ArtifactID]
			found := false
			if ok {
				for _, v := range target.versions {
					if v.Version == r.Version {
						found = true
					}
				}
			}
			if !found {
				return problemResponse(400, "RuleViolationException", "Referenced artifact does not exist.", client.RuleViolationCause{
					Description: fmt.Sprintf("Referenced artifact (%s/%s @ %s) does not yet exist in the registry.", r.GroupID, r.ArtifactID, r.Version),
					Context:     r.Name,
				})
			}
		}
	}
	if cfg := f.effectiveRule(a, client.RuleCompatibility); cfg != "" && cfg != "NONE" && a.Type == "AVRO" && prev != nil {
		if causes := avroBackwardCauses(string(prev.Content), content); len(causes) > 0 {
			return problemResponse(400, "RuleViolationException",
				fmt.Sprintf("Incompatible artifact: %s [AVRO], num of incompatible diffs: {%d}", a.ID, len(causes)), causes...)
		}
	}
	return nil, nil
}

// avroBackwardCauses flags new record fields without a default, the way the
// registry's BACKWARD check does.
// protoNamedBlock matches a named message, enum or service block.
var protoNamedBlock = regexp.MustCompile(`\b(message|enum|service)\s+[A-Za-z_][A-Za-z0-9_]*\s*\{`)

// protoSchemaValid is a shallow stand-in for the Protobuf parser (every
// VALIDITY level, like the registry): at least one named block and balanced
// braces.
func protoSchemaValid(content string) bool {
	return protoNamedBlock.MatchString(content) && strings.Count(content, "{") == strings.Count(content, "}")
}

// avroSchemaValid is a shallow stand-in for Avro's parser (every VALIDITY
// level other than NONE, like the registry, whose syntax check is the parser):
// a complex type must carry its defining member (record: fields, enum:
// symbols, array: items, map: values, fixed: size).
func avroSchemaValid(content string) bool {
	var v any
	if json.Unmarshal([]byte(content), &v) != nil {
		return false
	}
	switch s := v.(type) {
	case string:
		return s != ""
	case []any:
		return len(s) > 0
	case map[string]any:
		t, _ := s["type"].(string)
		required := map[string]string{"record": "fields", "enum": "symbols", "array": "items", "map": "values", "fixed": "size"}
		if member, ok := required[t]; ok {
			_, has := s[member]
			return has
		}
		return t != ""
	}
	return false
}

func avroBackwardCauses(oldContent, newContent string) []client.RuleViolationCause {
	var oldSchema, newSchema struct {
		Fields []map[string]any `json:"fields"`
	}
	if json.Unmarshal([]byte(oldContent), &oldSchema) != nil || json.Unmarshal([]byte(newContent), &newSchema) != nil {
		return nil
	}
	oldNames := map[string]bool{}
	for _, fld := range oldSchema.Fields {
		oldNames[asString(fld["name"])] = true
	}
	var causes []client.RuleViolationCause
	for i, fld := range newSchema.Fields {
		name := asString(fld["name"])
		if _, hasDefault := fld["default"]; !oldNames[name] && !hasDefault {
			causes = append(causes, client.RuleViolationCause{Description: name, Context: "/fields/" + strconv.Itoa(i)})
		}
	}
	return causes
}

func (f *fakeRegistry) handleVersions(m string, a *fakeArtifact, rest []string, q url.Values, body map[string]any) (*http.Response, error) {
	if len(rest) == 0 {
		switch m {
		case "GET":
			items := []map[string]any{}
			ids := make([]string, 0, len(a.versions))
			byID := map[string]*fakeVersion{}
			for _, v := range a.versions {
				ids = append(ids, v.Version)
				byID[v.Version] = v
			}
			for _, id := range page(ids, q) {
				items = append(items, f.versionOut(a, byID[id]))
			}
			return jsonResponse(200, map[string]any{"versions": items, "count": len(a.versions)})
		case "POST":
			v, resp, err := f.addVersion(a, body, false)
			if resp != nil || err != nil {
				return resp, err
			}
			if q.Get("dryRun") == "true" {
				a.versions = a.versions[:len(a.versions)-1]
				a.latest = removeString(a.latest, v.Version)
			}
			return jsonResponse(200, f.versionOut(a, v))
		}
		return problemResponse(405, "NotAllowedException", "method")
	}
	expr := rest[0]
	rest = rest[1:]
	v := f.resolveVersion(a, expr)
	if v == nil {
		shown := expr
		if strings.HasPrefix(expr, "branch=") {
			shown = "<tip of the branch '" + strings.TrimPrefix(expr, "branch=") + "'>"
		}
		return problemResponse(404, "VersionNotFoundException", fmt.Sprintf("No version '%s' found for artifact with ID '%s' in group '%s'.", shown, a.ID, a.GroupID))
	}
	if len(rest) == 0 {
		switch m {
		case "GET":
			return jsonResponse(200, f.versionOut(a, v))
		case "PUT":
			if val, ok := body["name"]; ok && val != nil {
				v.Name = asString(val)
			}
			if val, ok := body["description"]; ok && val != nil {
				v.Description = asString(val)
			}
			if val, ok := body["labels"]; ok && val != nil {
				v.Labels = labelsOf(val)
			}
			v.ModifiedOn = fakeNow()
			return noContent()
		case "DELETE":
			if f.props[client.PropertyVersionDeletion] != "true" {
				return problemResponse(405, "NotAllowedException", "Artifact version deletion operation is not enabled.")
			}
			kept := a.versions[:0]
			for _, cand := range a.versions {
				if cand != v {
					kept = append(kept, cand)
				}
			}
			a.versions = kept
			for _, b := range a.branches {
				b.Versions = removeString(b.Versions, v.Version)
			}
			a.latest = removeString(a.latest, v.Version)
			return noContent()
		}
		return problemResponse(405, "NotAllowedException", "method")
	}
	switch rest[0] {
	case "content":
		switch m {
		case "GET":
			if v.State == client.StateDisabled {
				return problemResponse(404, "VersionNotFoundException", fmt.Sprintf("No version '%s' found for artifact with ID '%s' in group '%s'.", v.Version, a.ID, a.GroupID))
			}
			resp := httpmock.NewBytesResponse(200, v.Content)
			ct := v.ContentType
			if strings.HasPrefix(ct, "text/plain") {
				ct = "text/plain;charset=UTF-8"
			}
			resp.Header.Set("Content-Type", ct)
			return resp, nil
		case "PUT":
			if f.props[client.PropertyContentMutability] != "true" {
				return problemResponse(405, "NotAllowedException", "Artifact version content update operation is not enabled.")
			}
			if v.State != client.StateDraft {
				return problemResponse(409, "ConflictException", "Requested artifact version is not in DRAFT state.  Update disallowed.")
			}
			v.Content = []byte(asString(body["content"]))
			v.ContentType = asString(body["contentType"])
			v.References = referencesOf(body["references"])
			f.nextContentID++ // still a draft: private content id
			v.ContentID = f.nextContentID
			v.ModifiedOn = fakeNow()
			return noContent()
		}
	case "references":
		if m == "GET" {
			refs := v.References
			if refs == nil || v.State == client.StateDisabled {
				refs = []client.Reference{}
			}
			return jsonResponse(200, refs)
		}
	case "state":
		if m == "PUT" {
			st := asString(body["state"])
			switch st {
			case client.StateDraft:
				return problemResponse(400, "BadRequestException", "Illegal state transition: cannot transition to DRAFT state.")
			case client.StateEnabled, client.StateDisabled, client.StateDeprecated, client.StateSunset:
			default:
				return problemResponse(400, "BadRequestException", "Not able to deserialize data provided.")
			}
			if st == v.State {
				return noContent()
			}
			if v.State == client.StateDraft {
				if resp, err := f.applyRulesAgainst(a, f.ruleTarget(a), string(v.Content), v.References); resp != nil || err != nil {
					return resp, err
				}
			}
			if q.Get("dryRun") != "true" {
				if v.State == client.StateDraft {
					v.ContentID, v.ContentType = f.contentID(string(v.Content), v.ContentType) // promoted: real, deduplicated id
					a.latest = append(a.latest, v.Version)                                     // joins latest now, as its tip
					a.latestBranch = true
				}
				v.State = st
				v.ModifiedOn = fakeNow()
			}
			return noContent()
		}
	}
	return problemResponse(404, "NotFoundException", "RESTEASY003210: Could not find resource for full path")
}

// resolveVersion resolves a version id or a `branch=<id>` expression.
func (f *fakeRegistry) resolveVersion(a *fakeArtifact, expr string) *fakeVersion {
	if !strings.HasPrefix(expr, "branch=") {
		for _, cand := range a.versions {
			if cand.Version == expr {
				return cand
			}
		}
		return nil
	}
	branch := strings.TrimPrefix(expr, "branch=")
	ids := f.branchVersionIDs(a, branch)
	if ids == nil {
		return nil
	}
	if branch == client.BranchIDLatest {
		return f.latestTip(a)
	}
	if len(ids) == 0 {
		return nil
	}
	return f.resolveVersion(a, ids[0])
}

// branchVersionIDs lists a branch newest-added first (nil for a missing branch).
func (f *fakeRegistry) branchVersionIDs(a *fakeArtifact, branch string) []string {
	ids := []string{}
	switch branch {
	case client.BranchIDLatest:
		if !a.latestBranch {
			return nil
		}
		for i := len(a.latest) - 1; i >= 0; i-- {
			ids = append(ids, a.latest[i])
		}
	case client.BranchIDDrafts:
		if !a.draftsBranch {
			return nil
		}
		for i := len(a.versions) - 1; i >= 0; i-- {
			if a.versions[i].State == client.StateDraft {
				ids = append(ids, a.versions[i].Version)
			}
		}
	default:
		b, ok := a.branches[branch]
		if !ok {
			return nil
		}
		for i := len(b.Versions) - 1; i >= 0; i-- {
			ids = append(ids, b.Versions[i])
		}
	}
	return ids
}

func (f *fakeRegistry) branchOut(a *fakeArtifact, id string) map[string]any {
	out := map[string]any{"groupId": a.GroupID, "artifactId": a.ID, "branchId": id, "owner": a.Owner, "modifiedBy": "",
		"createdOn": a.CreatedOn, "modifiedOn": a.CreatedOn, "systemDefined": true}
	if b, ok := a.branches[id]; ok {
		out["systemDefined"] = false
		out["createdOn"], out["modifiedOn"] = b.CreatedOn, b.ModifiedOn
		if b.Description != "" {
			out["description"] = b.Description
		}
	}
	return out
}

func (f *fakeRegistry) handleBranches(m string, a *fakeArtifact, rest []string, q url.Values, body map[string]any) (*http.Response, error) {
	if a.branches == nil {
		a.branches = map[string]*fakeBranch{}
	}
	isSystem := func(id string) bool { return id == client.BranchIDLatest || id == client.BranchIDDrafts }
	// the system branches appear with the first draft (drafts) / non-draft (latest) version
	exists := func(id string) bool {
		_, ok := a.branches[id]
		return ok || (id == client.BranchIDDrafts && a.draftsBranch) || (id == client.BranchIDLatest && a.latestBranch)
	}
	if len(rest) == 0 {
		switch m {
		case "GET":
			ids := []string{}
			if a.draftsBranch {
				ids = append(ids, client.BranchIDDrafts)
			}
			if a.latestBranch {
				ids = append(ids, client.BranchIDLatest)
			}
			for id := range a.branches {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			items := []map[string]any{}
			for _, id := range page(ids, q) {
				items = append(items, f.branchOut(a, id))
			}
			return jsonResponse(200, map[string]any{"branches": items, "count": len(ids)})
		case "POST":
			id := asString(body["branchId"])
			if !versionIDRe.MatchString(id) {
				return problemResponse(400, "ValidationException", fmt.Sprintf("Branch ID '%s' is invalid. It must consist of alphanumeric characters or '._-+', and have length 1..256 (inclusive).", id))
			}
			if exists(id) || isSystem(id) {
				return problemResponse(409, "BranchAlreadyExistsException", fmt.Sprintf("Branch '%s' already exists.", id))
			}
			var versions []string
			if raw, ok := body["versions"].([]any); ok {
				for _, r := range raw {
					vid := asString(r)
					if f.resolveVersion(a, vid) == nil {
						return problemResponse(404, "VersionNotFoundException", fmt.Sprintf("No version '%s' found for artifact with ID '%s' in group '%s'.", vid, a.ID, a.GroupID))
					}
					versions = append(versions, vid)
				}
			}
			a.branches[id] = &fakeBranch{ID: id, Description: asString(body["description"]), Versions: versions, CreatedOn: fakeNow(), ModifiedOn: fakeNow()}
			return jsonResponse(200, f.branchOut(a, id))
		}
		return problemResponse(405, "NotAllowedException", "method")
	}
	id := rest[0]
	rest = rest[1:]
	if !exists(id) {
		return problemResponse(404, "BranchNotFoundException", fmt.Sprintf("No branch '%s' was found in %s/%s.", id, a.GroupID, a.ID))
	}
	if len(rest) == 0 {
		switch m {
		case "GET":
			return jsonResponse(200, f.branchOut(a, id))
		case "PUT":
			if isSystem(id) {
				return problemResponse(409, "NotAllowedException", "System generated branches cannot be modified.")
			}
			b := a.branches[id]
			if val, ok := body["description"]; ok && val != nil {
				b.Description = asString(val)
			}
			b.ModifiedOn = fakeNow()
			return noContent()
		case "DELETE":
			if isSystem(id) {
				return problemResponse(409, "NotAllowedException", "System generated branches cannot be deleted.")
			}
			delete(a.branches, id)
			return noContent()
		}
		return problemResponse(405, "NotAllowedException", "method")
	}
	if rest[0] != "versions" {
		return problemResponse(404, "NotFoundException", "RESTEASY003210: Could not find resource for full path")
	}
	switch m {
	case "GET":
		ids := f.branchVersionIDs(a, id)
		items := []map[string]any{}
		for _, vid := range page(ids, q) {
			items = append(items, f.versionOut(a, f.resolveVersion(a, vid)))
		}
		return jsonResponse(200, map[string]any{"versions": items, "count": len(ids)})
	case "PUT", "POST":
		if isSystem(id) {
			return problemResponse(409, "NotAllowedException", "System generated branches cannot be modified.")
		}
		b := a.branches[id]
		var versions []string
		if m == "POST" {
			versions = append(append([]string{}, b.Versions...), asString(body["version"]))
		} else if raw, ok := body["versions"].([]any); ok {
			for _, r := range raw {
				versions = append(versions, asString(r))
			}
		} else {
			return problemResponse(400, "BadRequestException", "Not able to deserialize data provided.")
		}
		seen := map[string]bool{}
		for _, vid := range versions {
			if f.resolveVersion(a, vid) == nil {
				return problemResponse(500, "JdbcSQLIntegrityConstraintViolationException", "Referential integrity constraint violation: FK_BRANCH_VERSIONS_2")
			}
			if seen[vid] {
				return problemResponse(500, "JdbcSQLIntegrityConstraintViolationException", "Unique index or primary key violation: PRIMARY_KEY_D")
			}
			seen[vid] = true
		}
		b.Versions = versions
		b.ModifiedOn = fakeNow()
		return noContent()
	}
	return problemResponse(405, "NotAllowedException", "method")
}

// contentID returns the deduplicated content id of content and the content
// type of its first upload, registering new content.
func (f *fakeRegistry) contentID(content, contentType string) (int64, string) {
	sum := sha256.Sum256([]byte(content))
	h := hex.EncodeToString(sum[:])
	cid, ok := f.contentIDs[h]
	if !ok {
		f.nextContentID++
		cid = f.nextContentID
		f.contentIDs[h] = cid
		f.contentTypes[h] = contentType
	}
	return cid, f.contentTypes[h]
}

func removeString(list []string, s string) []string {
	out := list[:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func referencesOf(v any) []client.Reference {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	var refs []client.Reference
	for _, r := range raw {
		rm, _ := r.(map[string]any)
		refs = append(refs, client.Reference{
			GroupID: asString(rm["groupId"]), ArtifactID: asString(rm["artifactId"]), Version: asString(rm["version"]), Name: asString(rm["name"]),
		})
	}
	return refs
}

// ------------------------------------------------------------------ helpers

// detectArtifactType mirrors the registry: the content type hint decides
// which detectors run.
func detectArtifactType(contentType, content string) (string, bool) {
	ct := mediaType(contentType)
	switch {
	case strings.Contains(ct, "json"):
		var doc map[string]any
		if json.Unmarshal([]byte(content), &doc) != nil {
			return "", false
		}
		switch {
		case doc["openapi"] != nil || doc["swagger"] != nil:
			return "OPENAPI", true
		case doc["asyncapi"] != nil:
			return "ASYNCAPI", true
		case doc["type"] == "record" || doc["type"] == "enum" || doc["type"] == "fixed" || doc["type"] == "string":
			return "AVRO", true
		}
		return "JSON", true
	case strings.Contains(ct, "yaml"):
		var doc map[string]any
		if yaml.Unmarshal([]byte(content), &doc) != nil {
			return "", false
		}
		switch {
		case doc["openapi"] != nil || doc["swagger"] != nil:
			return "OPENAPI", true
		case doc["asyncapi"] != nil:
			return "ASYNCAPI", true
		}
		return "", false
	case strings.Contains(ct, "protobuf"):
		return "PROTOBUF", true
	case strings.Contains(ct, "graphql"):
		return "GRAPHQL", true
	case strings.Contains(ct, "xml"):
		switch {
		case strings.Contains(content, "XMLSchema"):
			return "XSD", true
		case strings.Contains(content, "wsdl"):
			return "WSDL", true
		}
		return "XML", true
	}
	return "", false
}

func labelsOf(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, val := range m {
		out[k] = asString(val)
	}
	return out
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// page applies the registry's offset/limit paging (limit defaults to 20).
func page(ids []string, q url.Values) []string {
	limit, offset := 20, 0
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 {
		limit = l
	}
	if o, err := strconv.Atoi(q.Get("offset")); err == nil && o >= 0 {
		offset = o
	}
	if offset >= len(ids) {
		return nil
	}
	end := offset + limit
	if end > len(ids) {
		end = len(ids)
	}
	return ids[offset:end]
}
