// resources_test.go —— docker.images / docker.volumes / docker.networks
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mow/mow/sdk"
)

func TestImagesCmd_ProjectsFields(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/images/json", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "true" {
			http.Error(w, `{"message":"expected all=true"}`, http.StatusBadRequest)
			return
		}
		body := []engineImage{
			{
				ID:       "sha256:abc",
				RepoTags: []string{"nginx:latest"},
				Size:     123456,
				Labels:   map[string]string{"env": "prod"},
			},
			{ID: "sha256:def", RepoTags: []string{"redis:7"}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	params, _ := json.Marshal(imagesParams{All: true})
	resp, err := (&imagesCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
		Params:     params,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out imagesResult
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Images) != 2 || out.Images[0].RepoTags[0] != "nginx:latest" {
		t.Fatalf("images: %+v", out.Images)
	}
	if out.Images[0].Labels["env"] != "prod" {
		t.Fatalf("labels not preserved: %+v", out.Images[0].Labels)
	}
}

func TestVolumesCmd_PassthroughWarnings(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/volumes", func(w http.ResponseWriter, r *http.Request) {
		body := engineVolumeList{
			Volumes: []engineVolume{
				{Name: "data", Driver: "local", Mountpoint: "/var/lib/docker/volumes/data/_data",
					Scope: "local", CreatedAt: "2024-01-01"},
			},
			Warnings: []string{"one warning"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	resp, err := (&volumesCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out volumesResult
	_ = json.Unmarshal(resp.Data, &out)
	if len(out.Volumes) != 1 || out.Volumes[0].Name != "data" {
		t.Fatalf("volumes: %+v", out.Volumes)
	}
	if len(out.Warnings) != 1 {
		t.Fatalf("warnings not passed: %+v", out.Warnings)
	}
}

func TestNetworksCmd_SubnetSummary(t *testing.T) {
	fe := newFakeEngine(t)
	fe.route("/networks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"Id":"n1","Name":"bridge","Driver":"bridge","Scope":"local",
			 "IPAM":{"Config":[{"Subnet":"172.17.0.0/16","Gateway":"172.17.0.1"}]}},
			{"Id":"n2","Name":"host","Driver":"host","Scope":"local","IPAM":{"Config":null}}
		]`))
	})
	resp, err := (&networksCmd{}).Execute(context.Background(), &sdk.ExecuteRequest{
		Connection: fe.connFor(t),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out networksResult
	_ = json.Unmarshal(resp.Data, &out)
	if len(out.Networks) != 2 {
		t.Fatalf("networks: %+v", out.Networks)
	}
	if len(out.Networks[0].SubnetSummary) != 1 || out.Networks[0].SubnetSummary[0] != "172.17.0.0/16" {
		t.Fatalf("subnet summary: %+v", out.Networks[0].SubnetSummary)
	}
	if len(out.Networks[1].SubnetSummary) != 0 {
		t.Fatalf("host network should have no subnet: %+v", out.Networks[1].SubnetSummary)
	}
}
