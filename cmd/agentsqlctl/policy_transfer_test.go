package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cuipengdba/agentsql/internal/policybundle"
)

func TestPolicyTransferCLIProducesProposalInputFromSignedSnapshots(t *testing.T) {
	directory := t.TempDir()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	seedPath := filepath.Join(directory, "seed.hex")
	publicPath := filepath.Join(directory, "public.hex")
	for path, value := range map[string][]byte{seedPath: private.Seed(), publicPath: public} {
		if err := os.WriteFile(path, []byte(hex.EncodeToString(value)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSnapshot := func(name string, bundle policybundle.Bundle) string {
		t.Helper()
		digest, err := policybundle.Digest(bundle)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, name)
		contents, err := json.Marshal(map[string]any{"code": 0, "msg": "ok", "data": map[string]any{"bundle": bundle, "digest": digest}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	source := policybundle.Bundle{SchemaVersion: policybundle.SchemaVersion, TenantID: "source", Policies: []policybundle.Entry{
		{ID: "source-rule", AgentID: "source-agent", DatasourceID: "source-db", ObjectType: "table", ObjectName: "public.orders", Action: "allow", Revision: 3},
	}}
	target := policybundle.Bundle{SchemaVersion: policybundle.SchemaVersion, TenantID: "target", Policies: []policybundle.Entry{}}
	sourcePath := writeSnapshot("source.json", source)
	targetPath := writeSnapshot("target.json", target)
	transferPath := filepath.Join(directory, "transfer.json")
	proposalPath := filepath.Join(directory, "proposal.json")
	var output, failures bytes.Buffer
	if code := run([]string{"policy-transfer", "sign", "--snapshot", sourcePath, "--source-environment", "staging", "--private-seed-file", seedPath, "--out", transferPath}, &output, &failures); code != 0 {
		t.Fatalf("sign failed (%d): %s", code, failures.String())
	}
	mappingPath := filepath.Join(directory, "mapping.json")
	mapping := policybundle.TransferMapping{TargetTenant: "target", Agents: map[string]string{"source-agent": "target-agent"},
		Datasources: map[string]string{"source-db": "target-db"}, Policies: map[string]string{"source-rule": "target-rule"},
		Objects: map[string]string{"source-rule": "public.orders"}}
	mappingJSON, _ := json.Marshal(mapping)
	if err := os.WriteFile(mappingPath, mappingJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	failures.Reset()
	planArgs := []string{"policy-transfer", "plan", "--transfer", transferPath, "--target-snapshot", targetPath,
		"--mapping", mappingPath, "--public-key-file", publicPath, "--source-environment", "staging", "--out", proposalPath}
	if code := run(planArgs, &output, &failures); code != 0 {
		t.Fatalf("plan failed (%d): %s", code, failures.String())
	}
	var proposal struct {
		BaseDigest string              `json:"base_digest"`
		Desired    policybundle.Bundle `json:"desired"`
	}
	contents, err := os.ReadFile(proposalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, &proposal); err != nil {
		t.Fatal(err)
	}
	if proposal.BaseDigest == "" || proposal.Desired.TenantID != "target" || len(proposal.Desired.Policies) != 1 ||
		proposal.Desired.Policies[0].ID != "target-rule" || proposal.Desired.Policies[0].AgentID != "target-agent" {
		t.Fatalf("wrong proposal: %#v", proposal)
	}
	planArgs[len(planArgs)-3] = "production"
	output.Reset()
	failures.Reset()
	if code := run(planArgs, &output, &failures); code == 0 {
		t.Fatal("wrong trusted source environment was accepted")
	}
}
