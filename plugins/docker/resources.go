// resources.go —— v0.3 第三阶段（Dashboard 侧）：镜像 / 卷 / 网络 只读列表。
//
// 与 docker.list 相似的透传实现：从 Engine 拉 JSON，投影出 UI 需要的字段。
// 均为 Read 权限，无需 Confirmed。
//
// - docker.images   GET /images/json?all=<bool>&filters=<json>
// - docker.volumes  GET /volumes
// - docker.networks GET /networks

package main

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// docker.images
// -----------------------------------------------------------------------------

type imagesParams struct {
	All bool `json:"all,omitempty"`
}

// imageEntry 是 UI 需要的镜像字段。
type imageEntry struct {
	ID          string            `json:"id"`
	ParentID    string            `json:"parent_id,omitempty"`
	RepoTags    []string          `json:"repo_tags,omitempty"`
	RepoDigests []string          `json:"repo_digests,omitempty"`
	Created     int64             `json:"created,omitempty"`
	Size        int64             `json:"size,omitempty"`
	VirtualSize int64             `json:"virtual_size,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Containers  int               `json:"containers,omitempty"`
}

// engineImage 对齐 /images/json（局部）。
type engineImage struct {
	ID          string            `json:"Id"`
	ParentID    string            `json:"ParentId"`
	RepoTags    []string          `json:"RepoTags"`
	RepoDigests []string          `json:"RepoDigests"`
	Created     int64             `json:"Created"`
	Size        int64             `json:"Size"`
	VirtualSize int64             `json:"VirtualSize"`
	Labels      map[string]string `json:"Labels"`
	Containers  int               `json:"Containers"`
}

type imagesResult struct {
	Images []imageEntry `json:"images"`
}

type imagesCmd struct{}

func (c *imagesCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "images",
		Description:    "list Docker images on the target engine",
		Permission:     sdk.PermRead,
		ConnectionType: "docker",
		Idempotent:     true,
	}
}
func (c *imagesCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}
func (c *imagesCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	if req.Connection == nil {
		return nil, sdk.ErrConnectionRequired
	}
	var p imagesParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	dt, err := resolveTarget(req.Connection)
	if err != nil {
		return nil, sdk.NewError("CONNECTION_INVALID", err.Error(), err)
	}
	cli, err := newEngineClient(dt)
	if err != nil {
		return nil, sdk.NewError("DOCKER_CLIENT_INVALID", err.Error(), err)
	}
	defer cli.closeIdle()

	q := url.Values{}
	if p.All {
		q.Set("all", "true")
	}
	var raw []engineImage
	if err := cli.getJSON(ctx, "/images/json", q, &raw); err != nil {
		return nil, err
	}
	out := imagesResult{Images: make([]imageEntry, 0, len(raw))}
	for _, e := range raw {
		out.Images = append(out.Images, imageEntry{
			ID: e.ID, ParentID: e.ParentID,
			RepoTags: e.RepoTags, RepoDigests: e.RepoDigests,
			Created: e.Created, Size: e.Size, VirtualSize: e.VirtualSize,
			Labels: e.Labels, Containers: e.Containers,
		})
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: data}, nil
}

// -----------------------------------------------------------------------------
// docker.volumes
// -----------------------------------------------------------------------------

// volumeEntry 是 UI 需要的卷字段。
type volumeEntry struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver,omitempty"`
	Mountpoint string            `json:"mountpoint,omitempty"`
	Scope      string            `json:"scope,omitempty"`
	CreatedAt  string            `json:"created_at,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Options    map[string]string `json:"options,omitempty"`
}

// engineVolume 对齐 /volumes 中每一项（局部）。
type engineVolume struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Mountpoint string            `json:"Mountpoint"`
	Scope      string            `json:"Scope"`
	CreatedAt  string            `json:"CreatedAt"`
	Labels     map[string]string `json:"Labels"`
	Options    map[string]string `json:"Options"`
}

type engineVolumeList struct {
	Volumes  []engineVolume `json:"Volumes"`
	Warnings []string       `json:"Warnings"`
}

type volumesResult struct {
	Volumes  []volumeEntry `json:"volumes"`
	Warnings []string      `json:"warnings,omitempty"`
}

type volumesCmd struct{}

func (c *volumesCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "volumes",
		Description:    "list Docker volumes on the target engine",
		Permission:     sdk.PermRead,
		ConnectionType: "docker",
		Idempotent:     true,
	}
}
func (c *volumesCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}
func (c *volumesCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	if req.Connection == nil {
		return nil, sdk.ErrConnectionRequired
	}
	dt, err := resolveTarget(req.Connection)
	if err != nil {
		return nil, sdk.NewError("CONNECTION_INVALID", err.Error(), err)
	}
	cli, err := newEngineClient(dt)
	if err != nil {
		return nil, sdk.NewError("DOCKER_CLIENT_INVALID", err.Error(), err)
	}
	defer cli.closeIdle()

	var raw engineVolumeList
	if err := cli.getJSON(ctx, "/volumes", nil, &raw); err != nil {
		return nil, err
	}
	out := volumesResult{Warnings: raw.Warnings, Volumes: make([]volumeEntry, 0, len(raw.Volumes))}
	for _, v := range raw.Volumes {
		out.Volumes = append(out.Volumes, volumeEntry{
			Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint,
			Scope: v.Scope, CreatedAt: v.CreatedAt,
			Labels: v.Labels, Options: v.Options,
		})
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: data}, nil
}

// -----------------------------------------------------------------------------
// docker.networks
// -----------------------------------------------------------------------------

// networkEntry 是 UI 需要的网络字段。
type networkEntry struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Driver     string            `json:"driver,omitempty"`
	Scope      string            `json:"scope,omitempty"`
	Internal   bool              `json:"internal,omitempty"`
	Attachable bool              `json:"attachable,omitempty"`
	Created    string            `json:"created,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	// SubnetSummary 是所有 IPAM.Config[i].Subnet 的拼接，便于列表页一栏展示。
	SubnetSummary []string `json:"subnet_summary,omitempty"`
}

type engineNetwork struct {
	ID         string `json:"Id"`
	Name       string `json:"Name"`
	Driver     string `json:"Driver"`
	Scope      string `json:"Scope"`
	Internal   bool   `json:"Internal"`
	Attachable bool   `json:"Attachable"`
	Created    string `json:"Created"`
	Labels     map[string]string `json:"Labels"`
	IPAM       struct {
		Config []struct {
			Subnet  string `json:"Subnet"`
			Gateway string `json:"Gateway"`
		} `json:"Config"`
	} `json:"IPAM"`
}

type networksResult struct {
	Networks []networkEntry `json:"networks"`
}

type networksCmd struct{}

func (c *networksCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "networks",
		Description:    "list Docker networks on the target engine",
		Permission:     sdk.PermRead,
		ConnectionType: "docker",
		Idempotent:     true,
	}
}
func (c *networksCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}
func (c *networksCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	if req.Connection == nil {
		return nil, sdk.ErrConnectionRequired
	}
	dt, err := resolveTarget(req.Connection)
	if err != nil {
		return nil, sdk.NewError("CONNECTION_INVALID", err.Error(), err)
	}
	cli, err := newEngineClient(dt)
	if err != nil {
		return nil, sdk.NewError("DOCKER_CLIENT_INVALID", err.Error(), err)
	}
	defer cli.closeIdle()

	var raw []engineNetwork
	if err := cli.getJSON(ctx, "/networks", nil, &raw); err != nil {
		return nil, err
	}
	out := networksResult{Networks: make([]networkEntry, 0, len(raw))}
	for _, n := range raw {
		var subnets []string
		for _, c := range n.IPAM.Config {
			if c.Subnet != "" {
				subnets = append(subnets, c.Subnet)
			}
		}
		out.Networks = append(out.Networks, networkEntry{
			ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: n.Scope,
			Internal: n.Internal, Attachable: n.Attachable,
			Created: n.Created, Labels: n.Labels, SubnetSummary: subnets,
		})
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: data}, nil
}
