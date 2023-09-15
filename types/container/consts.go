package container

type DependencyType string

const (
	LABEL_ENABLE         string         = "yacu.enable"
	LABEL_IMAGE_AGE      string         = "yacu.image_age"
	LABEL_STOP_TIMEOUT   string         = "yacu.stop_timeout"
	LABEL_DEPENDS_ON     string         = "com.docker.compose.depends_on"
	DEPENDENCY_STARTED   DependencyType = "service_started"
	DEPENDENCY_COMPLETED DependencyType = "service_completed_successfully"
	DEPENDENCY_HEALTHY   DependencyType = "service_healthy"
)
