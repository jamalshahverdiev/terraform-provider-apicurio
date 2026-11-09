package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
}

var fakeArtifactTypes = []string{"PROTOBUF", "OPENAPI", "ASYNCAPI", "JSON", "AVRO", "GRAPHQL", "KCONNECT", "WSDL", "XSD", "XML", "AGENT_CARD", "MCP_TOOL"}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		groups:    map[string]*fakeGroup{},
		artifacts: map[string]*fakeArtifact{},
		rules:     map[string]map[string]string{},
		props: map[string]string{
			client.PropertyGroupDeletion:    "true",
			client.PropertyArtifactDeletion: "true",
			client.PropertyVersionDeletion:  "true",
		},
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
	p := strings.TrimPrefix(req.URL.Path, client.APIPath)
	if p == req.URL.Path {
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
	case len(seg) >= 2 && seg[0] == "admin" && seg[1] == "rules":
		return f.handleRules(m, "", seg[2:], body)
	case seg[0] == "groups":
		return f.handleGroups(m, seg[1:], q, body)
	}
	return problemResponse(404, "NotFoundException", "RESTEASY003210: Could not find resource for full path: "+req.URL.String())
}

// ------------------------------------------------------------------ properties

func (f *fakeRegistry) handleProperties(m string, rest []string, body map[string]any) (*http.Response, error) {
	propOut := func(name string) map[string]any {
		return map[string]any{"name": name, "value": f.props[name], "type": "java.lang.Boolean", "label": name, "description": name}
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
		if v != "true" && v != "false" {
			return problemResponse(400, "InvalidPropertyValueException", "Invalid dynamic configuration property value for: "+name)
		}
		f.props[name] = v
		return noContent()
	case "DELETE":
		f.props[name] = "true" // the compose default
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
			if _, ok := rules[t]; ok {
				return problemResponse(409, "RuleAlreadyExistsException", fmt.Sprintf("A rule named '%s' already exists.", t))
			}
			rules[t] = cfg
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
		for k, a := range f.artifacts {
			if a.GroupID == groupID {
				delete(f.artifacts, k)
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
		"modifiedBy": "", "modifiedOn": v.CreatedOn}
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
	var refs []client.Reference
	if rawRefs, ok := content["references"].([]any); ok {
		for _, r := range rawRefs {
			rm, _ := r.(map[string]any)
			refs = append(refs, client.Reference{
				GroupID: asString(rm["groupId"]), ArtifactID: asString(rm["artifactId"]), Version: asString(rm["version"]), Name: asString(rm["name"]),
			})
		}
	}
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
	sum := sha256.Sum256([]byte(c))
	h := hex.EncodeToString(sum[:])
	cid, ok := f.contentIDs[h]
	if !ok {
		f.nextContentID++
		cid = f.nextContentID
		f.contentIDs[h] = cid
		f.contentTypes[h] = ct
	}
	ct = f.contentTypes[h] // deduplicated content keeps the type of its first upload
	state := "ENABLED"
	if isDraft {
		state = "DRAFT"
	}
	v := &fakeVersion{Version: versionID, GlobalID: f.nextGlobalID, ContentID: cid, State: state, ContentType: ct, Content: []byte(c), References: refs, CreatedOn: fakeNow()}
	v.Name, _ = in["name"].(string)
	v.Description, _ = in["description"].(string)
	v.Labels = labelsOf(in["labels"])
	a.versions = append(a.versions, v)
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
	if cfg := f.effectiveRule(a, client.RuleValidity); cfg != "" && cfg != "NONE" && jsonTypes[a.Type] {
		if !json.Valid([]byte(content)) {
			return problemResponse(400, "RuleViolationException", "Syntax violation for Avro artifact.")
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
	if cfg := f.effectiveRule(a, client.RuleCompatibility); cfg != "" && cfg != "NONE" && a.Type == "AVRO" && !first && len(a.versions) > 0 {
		prev := a.versions[len(a.versions)-1]
		if causes := avroBackwardCauses(string(prev.Content), content); len(causes) > 0 {
			return problemResponse(400, "RuleViolationException",
				fmt.Sprintf("Incompatible artifact: %s [AVRO], num of incompatible diffs: {%d}", a.ID, len(causes)), causes...)
		}
	}
	return nil, nil
}

// avroBackwardCauses flags new record fields without a default, the way the
// registry's BACKWARD check does.
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
			}
			return jsonResponse(200, f.versionOut(a, v))
		}
		return problemResponse(405, "NotAllowedException", "method")
	}
	expr := rest[0]
	rest = rest[1:]
	var v *fakeVersion
	switch {
	case expr == client.BranchLatest:
		if len(a.versions) > 0 {
			v = a.versions[len(a.versions)-1]
		}
	case strings.HasPrefix(expr, "branch="):
		v = nil
	default:
		for _, cand := range a.versions {
			if cand.Version == expr {
				v = cand
			}
		}
	}
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
			return noContent()
		}
		return problemResponse(405, "NotAllowedException", "method")
	}
	switch rest[0] {
	case "content":
		resp := httpmock.NewBytesResponse(200, v.Content)
		ct := v.ContentType
		if strings.HasPrefix(ct, "text/plain") {
			ct = "text/plain;charset=UTF-8"
		}
		resp.Header.Set("Content-Type", ct)
		return resp, nil
	case "references":
		refs := v.References
		if refs == nil {
			refs = []client.Reference{}
		}
		return jsonResponse(200, refs)
	}
	return problemResponse(404, "NotFoundException", "RESTEASY003210: Could not find resource for full path")
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
