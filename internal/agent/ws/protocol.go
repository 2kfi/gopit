// Package ws implements the agent's WebSocket control plane.
package ws

// Method names in the server<->agent protocol.
const (
	MethodAuth        = "auth"
	MethodSystemInfo  = "system.info"
	MethodSystemStats = "system.stats"

	MethodDockerContainersList    = "docker.containers.list"
	MethodDockerContainerInspect  = "docker.container.inspect"
	MethodDockerContainerStart    = "docker.container.start"
	MethodDockerContainerStop     = "docker.container.stop"
	MethodDockerContainerRemove   = "docker.container.remove"
	MethodDockerContainerLogs     = "docker.container.logs"
	MethodDockerContainerLogsStop = "docker.container.logs.stop"
	MethodDockerImagesList        = "docker.images.list"
	MethodDockerImageRemove       = "docker.image.remove"
	MethodDockerVolumesList       = "docker.volumes.list"
	MethodDockerVolumeRemove      = "docker.volume.remove"
	MethodDockerComposeList       = "docker.compose.list"
	MethodDockerComposeValidate   = "docker.compose.validate"
	MethodDockerComposeDeploy     = "docker.compose.deploy"
	MethodDockerComposeDown       = "docker.compose.down"
	MethodDockerComposePS         = "docker.compose.ps"

	MethodUfwStatus  = "ufw.status"
	MethodUfwRuleAdd = "ufw.rule.add"
	MethodUfwRuleDel = "ufw.rule.delete"
	MethodUfwToggle  = "ufw.toggle"
	MethodUfwPreview = "ufw.rule.preview"
)

// AuthPayload is the token handshake request body.
type AuthPayload struct {
	Token string `json:"token"`
}
