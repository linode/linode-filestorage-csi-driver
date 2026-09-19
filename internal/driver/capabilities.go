package driver

import csi "github.com/container-storage-interface/spec/lib/go/csi"

type AccessPolicyMode string

const (
	AccessPolicyModeSpace AccessPolicyMode = "space"
	AccessPolicyModeNode  AccessPolicyMode = "node"
)

func pluginCapabilities(role Role) []*csi.PluginCapability {
	var capabilities []csi.PluginCapability_Service_Type

	switch role {
	case RoleController:
		capabilities = []csi.PluginCapability_Service_Type{
			csi.PluginCapability_Service_CONTROLLER_SERVICE,
		}
		// Future post-v1 capability: advertise plugin volume expansion once
		// quota-backed resize semantics are implemented end to end.
		// expansion := csi.PluginCapability_VolumeExpansion_OFFLINE
	case RoleNode:
	}

	pc := make([]*csi.PluginCapability, 0, len(capabilities))
	for _, c := range capabilities {
		pc = append(pc, &csi.PluginCapability{
			Type: &csi.PluginCapability_Service_{
				Service: &csi.PluginCapability_Service{Type: c},
			},
		})
	}
	return pc
}

func controllerServiceCapabilities(accessPolicyMode AccessPolicyMode) []*csi.ControllerServiceCapability {
	capabilities := []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
	}
	if accessPolicyMode == AccessPolicyModeNode {
		capabilities = append(capabilities, csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME)
	}
	capabilities = append(capabilities,
		csi.ControllerServiceCapability_RPC_GET_VOLUME,
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
	)

	// Future post-v1 capabilities once core filesystem lifecycle is complete.
	// capabilities = append(capabilities,
	// 	csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
	// 	csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
	// 	csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
	// )

	cc := make([]*csi.ControllerServiceCapability, 0, len(capabilities))
	for _, c := range capabilities {
		cc = append(cc, &csi.ControllerServiceCapability{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{Type: c},
			},
		})
	}
	return cc
}

func nodeServiceCapabilities() []*csi.NodeServiceCapability {
	capabilities := []csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
	}

	nc := make([]*csi.NodeServiceCapability, 0, len(capabilities))
	for _, c := range capabilities {
		nc = append(nc, &csi.NodeServiceCapability{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{Type: c},
			},
		})
	}
	return nc
}
