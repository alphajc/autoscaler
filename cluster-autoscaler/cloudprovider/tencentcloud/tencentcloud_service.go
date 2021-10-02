/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tencentcloud

import (
	"fmt"
	"time"

	as "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/as/v20180419"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	tke "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tke/v20180525"
	vpc "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc/v20170312"
	"k8s.io/klog/v2"

	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/tencentcloud/metrics"
)

// CloudService is used for communicating with Tencentcloud API.
type CloudService interface {
	// FetchAsgInstances returns instances of the specified ASG.
	FetchAsgInstances(TencentcloudRef) ([]cloudprovider.Instance, error)
	// DeleteInstances remove instances of specified ASG.
	DeleteInstances(Asg, []string) error
	// GetAsgRefByInstanceRef returns asgRef according to instanceRef
	GetAsgRefByInstanceRef(TencentcloudRef) (TencentcloudRef, error)
	// GetAutoScalingGroups queries and returns a set of ASG.
	GetAutoScalingGroups([]string) ([]as.AutoScalingGroup, error)
	// GetAutoscalingConfigs queries and returns a set of ASG launchconfiguration.
	GetAutoscalingConfigs([]string) ([]as.LaunchConfiguration, error)
	// GetAutoScalingGroups queries and returns a set of ASG.
	GetAutoScalingGroup(TencentcloudRef) (as.AutoScalingGroup, error)
	// ResizeAsg set the target size of ASG.
	ResizeAsg(TencentcloudRef, uint64) error
	// GetAutoScalingInstances returns instances of specific ASG.
	GetAutoScalingInstances(TencentcloudRef) ([]*as.Instance, error)
	// GetTencentcloudInstanceRef returns a Tencentcloud ref.
	GetTencentcloudInstanceRef(*as.Instance) (TencentcloudRef, error)
	// DescribeVpcCniPodLimits list network limits
	DescribeVpcCniPodLimits(string) (*tke.PodLimitsInstance, error)
	// GetInstanceInfoByType queries the number of CPU, memory, and GPU resources of the model configured for generating template
	GetInstanceInfoByType(string) (*InstanceInfo, error)
	// GetNodePoolInfo returns nodepool information from TKE.
	GetNodePoolInfo(string, string) (*NodePoolInfo, error)
	// GetZoneInfo invokes cvm.DescribeZones to query zone information.
	GetZoneInfo(string) (*cvm.ZoneInfo, error)
}

// TencentcloudService provides several utility methods over the auto-scaling cloudService provided by Tencentcloud SDK
type TencentcloudService struct {
	asClient  *as.Client
	cvmClient *cvm.Client
	tkeClient *tke.Client
	vpcClient *vpc.Client
}

const (
	maxRecordsReturnedByAPI = 100
)

// CVM stop mode
const (
	StoppedModeStopCharging = "STOP_CHARGING"
	StoppedModeKeepCharging = "KEEP_CHARGING"
)

// AS scaling mode
const (
	ScalingModeClassic       = "CLASSIC_SCALING"
	ScalingModeWakeUpStopped = "WAKE_UP_STOPPED_SCALING"
)

var zoneInfos = make(map[string]*cvm.ZoneInfo)

// SubnetInfo represents subnet's detail
type SubnetInfo struct {
	SubnetID string
	Zone     string
	ZoneID   int
}

// FetchAsgInstances returns instances of the specified ASG.
func (ts *TencentcloudService) FetchAsgInstances(asgRef TencentcloudRef) ([]cloudprovider.Instance, error) {
	tencentcloudInstances, err := ts.GetAutoScalingInstances(asgRef)
	if err != nil {
		klog.V(4).Infof("Failed ASG info request for %s %s: %v", asgRef.Zone, asgRef.ID, err)
		return nil, err
	}
	infos := []cloudprovider.Instance{}
	for _, instance := range tencentcloudInstances {
		ref, err := ts.GetTencentcloudInstanceRef(instance)
		if err != nil {
			return nil, err
		}
		infos = append(infos, cloudprovider.Instance{
			Id: ref.ToProviderId(),
		})
	}
	return infos, nil
}

// DeleteInstances remove instances of specified ASG.
// NOTICE: 一般情况下都是移除一个节点，只有在创建失败时才有批量移除，暂未分页处理
// 目前都是调的 as 接口，如果是关机模式的伸缩组进行关机操作，其它进行移除操作
func (ts *TencentcloudService) DeleteInstances(asg Asg, instances []string) error {
	// TODO 处理缩容保护

	if asg.GetScalingType() == ScalingModeWakeUpStopped {
		return ts.stopInstances(asg, instances)
	}

	return ts.removeInstances(asg, instances)
}

// GetAutoscalingConfigs queries and returns a set of ASG launchconfiguration.
func (ts *TencentcloudService) GetAutoscalingConfigs(ascs []string) ([]as.LaunchConfiguration, error) {
	if ts.asClient == nil {
		return nil, fmt.Errorf("asClient is not initialized")
	}

	if len(ascs) > 100 {
		klog.Warning("The number of Launch Configuration IDs exceeds 100: ", len(ascs))
	}

	// 查询AS，启动配置对应机型
	req := as.NewDescribeLaunchConfigurationsRequest()
	req.LaunchConfigurationIds = common.StringPtrs(ascs)
	resp, err := ts.asClient.DescribeLaunchConfigurations(req)
	metrics.RegisterCloudAPIInvoked("as", "DescribeLaunchConfigurations", err)
	if err != nil {
		return nil, err
	}

	if resp == nil || resp.Response == nil ||
		resp.Response.LaunchConfigurationSet == nil {
		return nil, fmt.Errorf("DescribeLaunchConfigurations returned a invalid response")
	}

	res := make([]as.LaunchConfiguration, 0)
	for _, lc := range resp.Response.LaunchConfigurationSet {
		if lc != nil {
			res = append(res, *lc)
		}
	}

	if len(res) != len(ascs) {
		return nil, fmt.Errorf("DescribeLaunchConfigurations need: %d, real: %d", len(ascs), len(res))
	}

	return res, nil
}

// InstanceInfo represents CVM's detail
type InstanceInfo struct {
	CPU            int64
	Memory         int64
	GPU            int64
	InstanceFamily string
	InstanceType   string
	Zone           string
}

// GetInstanceInfoByType queries the number of CPU, memory, and GPU resources of the model configured for generating template
func (ts *TencentcloudService) GetInstanceInfoByType(instanceType string) (*InstanceInfo, error) {
	if ts.cvmClient == nil {
		return nil, fmt.Errorf("cvmClient is not initialized")
	}

	// DescribeZoneInstanceConfigInfos
	instanceTypeRequest := cvm.NewDescribeInstanceTypeConfigsRequest()
	instanceTypeRequest.Filters = []*cvm.Filter{
		{
			Name:   common.StringPtr("instance-type"),
			Values: []*string{&instanceType},
		},
	}

	resp, err := ts.cvmClient.DescribeInstanceTypeConfigs(instanceTypeRequest)
	metrics.RegisterCloudAPIInvoked("cvm", "DescribeInstanceTypeConfigs", err)
	if err != nil {
		return nil, err
	}

	if resp == nil ||
		resp.Response == nil ||
		resp.Response.InstanceTypeConfigSet == nil ||
		len(resp.Response.InstanceTypeConfigSet) < 1 ||
		resp.Response.InstanceTypeConfigSet[0].CPU == nil ||
		resp.Response.InstanceTypeConfigSet[0].Memory == nil ||
		resp.Response.InstanceTypeConfigSet[0].GPU == nil ||
		resp.Response.InstanceTypeConfigSet[0].InstanceFamily == nil ||
		resp.Response.InstanceTypeConfigSet[0].InstanceType == nil ||
		resp.Response.InstanceTypeConfigSet[0].Zone == nil {
		return nil, fmt.Errorf("DescribeInstanceTypeConfigs returned a invalid response")
	}

	return &InstanceInfo{
		*resp.Response.InstanceTypeConfigSet[0].CPU,
		*resp.Response.InstanceTypeConfigSet[0].Memory,
		*resp.Response.InstanceTypeConfigSet[0].GPU,
		*resp.Response.InstanceTypeConfigSet[0].InstanceFamily,
		*resp.Response.InstanceTypeConfigSet[0].InstanceType,
		*resp.Response.InstanceTypeConfigSet[0].Zone,
	}, nil
}

// GetAutoScalingGroups queries and returns a set of ASG.
func (ts *TencentcloudService) GetAutoScalingGroups(asgIds []string) ([]as.AutoScalingGroup, error) {
	if ts.asClient == nil {
		return nil, fmt.Errorf("asClient is not initialized")
	}

	if len(asgIds) > 100 {
		klog.Warning("The number of ASG IDs exceeds 100: ", len(asgIds))
	}

	req := as.NewDescribeAutoScalingGroupsRequest()
	req.AutoScalingGroupIds = common.StringPtrs(asgIds)

	response, err := ts.asClient.DescribeAutoScalingGroups(req)
	metrics.RegisterCloudAPIInvoked("as", "DescribeAutoScalingGroups", err)
	if err != nil {
		return nil, err
	}

	if response == nil || response.Response == nil || response.Response.AutoScalingGroupSet == nil {
		return nil, fmt.Errorf("DescribeAutoScalingGroups returned a invalid response")
	}

	asgs := make([]as.AutoScalingGroup, 0)
	for _, asg := range response.Response.AutoScalingGroupSet {
		if asg != nil {
			asgs = append(asgs, *asg)
		}
	}

	return asgs, nil
}

// GetAutoScalingGroup returns the specific ASG.
func (ts *TencentcloudService) GetAutoScalingGroup(asgRef TencentcloudRef) (as.AutoScalingGroup, error) {
	if ts.asClient == nil {
		return as.AutoScalingGroup{}, fmt.Errorf("asClient is not initialized")
	}

	req := as.NewDescribeAutoScalingGroupsRequest()
	req.AutoScalingGroupIds = []*string{&asgRef.ID}
	resp, err := ts.asClient.DescribeAutoScalingGroups(req)
	metrics.RegisterCloudAPIInvoked("as", "DescribeAutoScalingGroups", err)
	if err != nil {
		return as.AutoScalingGroup{}, err
	}

	if resp == nil ||
		resp.Response == nil ||
		resp.Response.AutoScalingGroupSet == nil ||
		len(resp.Response.AutoScalingGroupSet) != 1 ||
		resp.Response.AutoScalingGroupSet[0] == nil {
		return as.AutoScalingGroup{}, fmt.Errorf("DescribeAutoScalingGroups returned a invalid response")
	}

	return *resp.Response.AutoScalingGroupSet[0], nil
}

// GetAutoScalingInstances returns instances of specific ASG.
func (ts *TencentcloudService) GetAutoScalingInstances(asgRef TencentcloudRef) ([]*as.Instance, error) {
	if ts.asClient == nil {
		return nil, fmt.Errorf("asClient is not initialized")
	}

	req := as.NewDescribeAutoScalingInstancesRequest()
	filter := as.Filter{
		Name:   common.StringPtr("auto-scaling-group-id"),
		Values: common.StringPtrs([]string{asgRef.ID}),
	}
	req.Filters = []*as.Filter{&filter}
	req.Limit = common.Int64Ptr(maxRecordsReturnedByAPI)
	req.Offset = common.Int64Ptr(0)
	resp, err := ts.asClient.DescribeAutoScalingInstances(req)
	metrics.RegisterCloudAPIInvoked("as", "DescribeAutoScalingInstances", err)
	if err != nil {
		return nil, err
	}
	res := resp.Response.AutoScalingInstanceSet
	totalCount := uint64(0)
	if resp.Response.TotalCount != nil {
		totalCount = *resp.Response.TotalCount
	}
	for uint64(len(res)) < totalCount {
		req.Offset = common.Int64Ptr(int64(len(res)))
		resp, err = ts.asClient.DescribeAutoScalingInstances(req)
		metrics.RegisterCloudAPIInvoked("as", "DescribeAutoScalingInstances", err)
		if err != nil {
			return nil, err
		}
		res = append(res, resp.Response.AutoScalingInstanceSet...)
	}
	return res, nil
}

// GetAsgRefByInstanceRef returns asgRef according to instanceRef
func (ts *TencentcloudService) GetAsgRefByInstanceRef(instanceRef TencentcloudRef) (TencentcloudRef, error) {
	if ts.asClient == nil {
		return TencentcloudRef{}, fmt.Errorf("asClient is not initialized")
	}

	req := as.NewDescribeAutoScalingInstancesRequest()
	req.InstanceIds = common.StringPtrs([]string{instanceRef.ID})
	resp, err := ts.asClient.DescribeAutoScalingInstances(req)
	metrics.RegisterCloudAPIInvoked("as", "DescribeAutoScalingInstances", err)
	if err != nil {
		return TencentcloudRef{}, err
	}

	if resp == nil || resp.Response == nil ||
		resp.Response.AutoScalingInstanceSet == nil ||
		resp.Response.TotalCount == nil ||
		*resp.Response.TotalCount != 1 ||
		len(resp.Response.AutoScalingInstanceSet) != 1 ||
		resp.Response.AutoScalingInstanceSet[0] == nil ||
		resp.Response.AutoScalingInstanceSet[0].AutoScalingGroupId == nil {
		return TencentcloudRef{}, fmt.Errorf("DescribeAutoScalingInstances response is invalid: %+v", resp)
	}

	return TencentcloudRef{
		ID: *resp.Response.AutoScalingInstanceSet[0].AutoScalingGroupId,
	}, nil
}

// ResizeAsg set the target size of ASG.
func (ts *TencentcloudService) ResizeAsg(ref TencentcloudRef, size uint64) error {
	if ts.asClient == nil {
		return fmt.Errorf("asClient is not initialized")
	}
	req := as.NewModifyAutoScalingGroupRequest()
	req.AutoScalingGroupId = common.StringPtr(ref.ID)
	req.DesiredCapacity = common.Uint64Ptr(size)
	resp, err := ts.asClient.ModifyAutoScalingGroup(req)
	metrics.RegisterCloudAPIInvoked("as", "ModifyAutoScalingGroup", err)
	if err != nil {
		return err
	}
	if resp == nil || resp.Response == nil ||
		resp.Response.RequestId == nil {
		return fmt.Errorf("ModifyAutoScalingGroup returned a invalid response")
	}

	klog.V(4).Infof("ResizeAsg size %d, requestID: %s", size, *resp.Response.RequestId)

	return nil
}

// GetZoneInfo invokes cvm.DescribeZones to query zone information.
// zoneInfo will be cache.
func (ts *TencentcloudService) GetZoneInfo(zone string) (*cvm.ZoneInfo, error) {
	if zoneInfo, exist := zoneInfos[zone]; exist {
		return zoneInfo, nil
	}
	req := cvm.NewDescribeZonesRequest()
	resp, err := ts.cvmClient.DescribeZones(req)
	metrics.RegisterCloudAPIInvoked("cvm", "DescribeZones", err)
	if err != nil {
		return nil, err
	} else if resp.Response == nil || resp.Response.TotalCount == nil || *resp.Response.TotalCount < 1 {
		return nil, fmt.Errorf("DescribeZones returns a invalid response")
	}
	for _, it := range resp.Response.ZoneSet {
		if it != nil && it.Zone != nil && *it.Zone == zone {
			zoneInfos[zone] = it
			return it, nil
		}
	}
	return nil, fmt.Errorf("%s is not exist", zone)
}

// GetTencentcloudInstanceRef returns a Tencentcloud ref.
func (ts *TencentcloudService) GetTencentcloudInstanceRef(instance *as.Instance) (TencentcloudRef, error) {
	if instance == nil {
		return TencentcloudRef{}, fmt.Errorf("instance is nil")
	}

	zoneID := ""
	if instance.Zone != nil && *instance.Zone != "" {
		zoneInfo, err := ts.GetZoneInfo(*instance.Zone)
		if err != nil {
			return TencentcloudRef{}, err
		}
		zoneID = *zoneInfo.ZoneId
	}

	return TencentcloudRef{
		ID:   *instance.InstanceId,
		Zone: zoneID,
	}, nil
}

// NodePoolInfo represents the information nodePool or clusterAsg from dashboard
type NodePoolInfo struct {
	Labels []*tke.Label
	Taints []*tke.Taint
}

// GetNodePoolInfo returns nodepool information from TKE.
func (ts *TencentcloudService) GetNodePoolInfo(clusterID string, asgID string) (*NodePoolInfo, error) {
	if ts.tkeClient == nil {
		return nil, fmt.Errorf("tkeClient is not initialized")
	}
	// From NodePool
	npReq := tke.NewDescribeClusterNodePoolsRequest()
	npReq.ClusterId = common.StringPtr(clusterID)
	npResp, err := ts.tkeClient.DescribeClusterNodePools(npReq)
	if e, ok := err.(*errors.TencentCloudSDKError); ok {
		metrics.RegisterCloudAPIInvokedError("tke", "DescribeClusterNodePools", e.Code)
		return nil, e
	}
	if npResp == nil || npResp.Response == nil || npResp.Response.RequestId == nil {
		return nil, errors.NewTencentCloudSDKError("DASHBOARD_ERROR", "empty response", "-")
	}
	var targetNodePool *tke.NodePool
	for _, np := range npResp.Response.NodePoolSet {
		if np.AutoscalingGroupId != nil && *np.AutoscalingGroupId == asgID {
			targetNodePool = np
			break
		}
	}
	if targetNodePool != nil {
		return &NodePoolInfo{
			Labels: targetNodePool.Labels,
			Taints: targetNodePool.Taints,
		}, nil
	}

	// Compatible with DEPRECATED autoScalingGroups
	asgReq := tke.NewDescribeClusterAsGroupsRequest()
	asgReq.AutoScalingGroupIds = []*string{common.StringPtr(asgID)}
	asgReq.ClusterId = common.StringPtr(clusterID)
	asgResp, err := ts.tkeClient.DescribeClusterAsGroups(asgReq)
	if err != nil {
		if e, ok := err.(*errors.TencentCloudSDKError); ok {
			metrics.RegisterCloudAPIInvokedError("tke", "DescribeClusterAsGroups", e.Code)
		}
		return nil, err
	}
	if asgResp == nil || asgResp.Response == nil {
		return nil, errors.NewTencentCloudSDKError("DASHBOARD_ERROR", "empty response", "-")
	}
	asgCount := len(asgResp.Response.ClusterAsGroupSet)
	if asgCount != 1 {
		return nil, errors.NewTencentCloudSDKError("UNEXPECTED_ERROR",
			fmt.Sprintf("%s get %d autoScalingGroup", asgID, asgCount), *asgResp.Response.RequestId)
	}
	asg := asgResp.Response.ClusterAsGroupSet[0]
	if asg == nil {
		return nil, errors.NewTencentCloudSDKError("UNEXPECTED_ERROR", "asg is nil", *asgResp.Response.RequestId)
	}

	return &NodePoolInfo{
		Labels: asg.Labels,
	}, nil
}

// DescribeVpcCniPodLimits list network limits
func (ts *TencentcloudService) DescribeVpcCniPodLimits(instanceType string) (*tke.PodLimitsInstance, error) {
	if ts.tkeClient == nil {
		return nil, fmt.Errorf("tkeClient is not initialized")
	}
	req := tke.NewDescribeVpcCniPodLimitsRequest()
	req.InstanceType = common.StringPtr(instanceType)
	resp, err := ts.tkeClient.DescribeVpcCniPodLimits(req)
	if err != nil {
		if e, ok := err.(*errors.TencentCloudSDKError); ok {
			metrics.RegisterCloudAPIInvokedError("tke", "DescribeVpcCniPodLimits", e.Code)
		}
		return nil, err
	}
	if resp == nil || resp.Response == nil || resp.Response.RequestId == nil {
		return nil, errors.NewTencentCloudSDKError("DASHBOARD_ERROR", "empty response", "-")
	}
	if len(resp.Response.PodLimitsInstanceSet) == 0 {
		return nil, nil
	}

	// PodLimitsInstanceSet 分可用区返回，会存在多组值，不过内容都一样，取第一个
	return resp.Response.PodLimitsInstanceSet[0], nil
}

func (ts *TencentcloudService) stopAutoScalingInstancesWithRetry(req *as.StopAutoScalingInstancesRequest) error {
	var err error
	scalingActivityId := ""
	for i := 0; i < retryCountStop; i++ {
		if i > 0 {
			time.Sleep(intervalTimeStop)
		}
		var resp = &as.StopAutoScalingInstancesResponse{}
		resp, err = ts.asClient.StopAutoScalingInstances(req)
		metrics.RegisterCloudAPIInvoked("as", "StopAutoScalingInstances", err)
		if err != nil {
			if asErr, ok := err.(*errors.TencentCloudSDKError); ok {
				// 仍然有不支持的
				if asErr.Code == "ResourceUnavailable.StoppedInstanceWithInconsistentChargingMode" {
					continue
				}
			}
			// 如果错误不是因为机型的原因，就重试
			klog.Warningf("StopAutoScalingInstances failed %v, %d retry", err.Error(), i)
		} else {
			if resp.Response.ActivityId != nil {
				scalingActivityId = *resp.Response.ActivityId
			}
			break
		}
	}

	if err != nil {
		return err
	}

	// check activity
	err = ts.ensureAutoScalingActivityDone(scalingActivityId)
	if err != nil {
		return err
	}

	return nil
}

// removeInstances invoke as.RemoveInstances
// api document: https://cloud.tencent.com/document/api/377/20431
func (ts *TencentcloudService) removeInstances(asg Asg, instances []string) error {
	if ts.asClient == nil {
		return fmt.Errorf("asClient is not initialized")
	}

	req := as.NewRemoveInstancesRequest()
	req.AutoScalingGroupId = common.StringPtr(asg.Id())
	req.InstanceIds = common.StringPtrs(instances)

	resp, err := ts.asClient.RemoveInstances(req)
	metrics.RegisterCloudAPIInvoked("as", "RemoveInstances", err)
	if err != nil {
		return err
	}
	if resp == nil || resp.Response == nil ||
		resp.Response.ActivityId == nil ||
		resp.Response.RequestId == nil {
		return fmt.Errorf("RemoveInstances returned a invalid response")
	}
	klog.V(4).Infof("Remove instances %v, asaID: %s, requestID: %s", instances, *resp.Response.ActivityId, *resp.Response.RequestId)

	return nil
}

// stopInstances 关闭instanceList中符合关机不收费的机型的机器，如果不支持的机器，就进行关机收费操作
// TODO 注意：该方法仅供上层调用，instanceList的长度需要上层控制，长度最大限制：100
func (ts *TencentcloudService) stopInstances(asg Asg, instances []string) error {
	if ts.asClient == nil {
		return fmt.Errorf("asClient is not initialized")
	}
	req := as.NewStopAutoScalingInstancesRequest()
	req.AutoScalingGroupId = common.StringPtr(asg.Id())
	req.InstanceIds = common.StringPtrs(instances)
	req.StoppedMode = common.StringPtr(StoppedModeStopCharging)

	// 没有dry run，所以先试跑一次，如果超过了，就直接超过了，
	keepChargingIns := make([]string, 0)
	stopChargingIns := make([]string, 0)
	var errOut error
	scalingActivityId := ""
	for i := 0; i < retryCountStop; i++ {
		// 从第二次开始，等待5s钟（一般autoscaling移出节点的时间为3s）
		if i > 0 {
			time.Sleep(intervalTimeStop)
		}
		resp, err := ts.asClient.StopAutoScalingInstances(req)
		metrics.RegisterCloudAPIInvoked("as", "StopAutoScalingInstances", err)
		if e, ok := err.(*errors.TencentCloudSDKError); ok {
			metrics.RegisterCloudAPIInvokedError("as", "StopAutoScalingInstances", e.Code)
		}
		if err == nil {
			// 一次成功
			klog.Info("StopAutoScalingInstances succeed")
			klog.V(4).Infof("res:%#v", resp.Response)
			if resp.Response.ActivityId != nil {
				scalingActivityId = *resp.Response.ActivityId
			}
			break
		} else if asErr, ok := err.(*errors.TencentCloudSDKError); ok &&
			(asErr.Code == "ResourceUnavailable.StoppedInstanceWithInconsistentChargingMode" ||
				asErr.Code == "ResourceUnavailable.InstanceNotSupportStopCharging") { // TODO 这里拿code和msg返回做判断，有点危险
			stopChargingIns, keepChargingIns = getInstanceIdsFromMessage(instances, asErr.Message)
			break
		} else {
			errOut = err
			klog.Warningf("Failed to StopAutoScalingInstances res:%#v", err)
		}
	}

	// 如果是一次就过了，说明instance是全部可以关机不收费的，直接结束
	if errOut == nil && scalingActivityId != "" {
		// check activity
		err := ts.ensureAutoScalingActivityDone(scalingActivityId)
		if err != nil {
			return err
		}
		return nil
	} else if errOut != nil {
		// 如果一直在报其他的错误，就返回错误
		return errOut
	}
	// 如果有不支持关机不收费的话，就分别执行两次。

	// 支持关机不收费的实例，进行`StopCharging`操作
	if len(stopChargingIns) != 0 {
		req.InstanceIds = common.StringPtrs(stopChargingIns)
		req.StoppedMode = common.StringPtr(StoppedModeStopCharging)
		err := ts.stopAutoScalingInstancesWithRetry(req)
		if err != nil {
			errOut = err
		}
	}

	if len(keepChargingIns) != 0 {
		// 不支持关机不收费的实例，进行`KeepCharging`操作
		req.InstanceIds = common.StringPtrs(keepChargingIns)
		req.StoppedMode = common.StringPtr(StoppedModeKeepCharging)
		err := ts.stopAutoScalingInstancesWithRetry(req)
		if err != nil {
			errOut = err
		}
	}
	if errOut != nil {
		return errOut
	}
	return nil
}

func (ts *TencentcloudService) ensureAutoScalingActivityDone(scalingActivityId string) error {
	if scalingActivityId == "" {
		return fmt.Errorf("ActivityId is nil")
	}

	checker := func(r interface{}, e error) bool {
		if e != nil {
			return false
		}
		resp, ok := r.(*as.DescribeAutoScalingActivitiesResponse)
		if !ok || resp.Response == nil || len(resp.Response.ActivitySet) != 1 {
			return false
		}
		if resp.Response.ActivitySet[0].StatusCode != nil {
			if *resp.Response.ActivitySet[0].StatusCode == "INIT" || *resp.Response.ActivitySet[0].StatusCode == "RUNNING" {
				return false
			}
			return true
		}
		return true
	}
	do := func() (interface{}, error) {
		if ts.asClient == nil {
			return nil, fmt.Errorf("asClient is not initialized")
		}
		req := as.NewDescribeAutoScalingActivitiesRequest()
		req.ActivityIds = common.StringPtrs([]string{scalingActivityId})

		resp, err := ts.asClient.DescribeAutoScalingActivities(req)
		metrics.RegisterCloudAPIInvoked("as", "DescribeAutoScalingActivities", err)
		if err != nil {
			return nil, err
		}
		return resp, nil
	}

	ret, isTimeout, err := retryDo(do, checker, 1200, 2)
	if err != nil {
		return fmt.Errorf("EnsureAutoScalingActivityDone scalingActivityId:%s failed:%v", scalingActivityId, err)
	}

	if isTimeout {
		return fmt.Errorf("EnsureAutoScalingActivityDone scalingActivityId:%s timeout", scalingActivityId)
	}
	resp, ok := ret.(*as.DescribeAutoScalingActivitiesResponse)
	if !ok || resp.Response == nil || len(resp.Response.ActivitySet) != 1 {
		return fmt.Errorf("EnsureAutoScalingActivityDone scalingActivityId:%s failed", scalingActivityId)
	}
	if resp.Response.ActivitySet[0].StatusCode != nil && *resp.Response.ActivitySet[0].StatusCode != "SUCCESSFUL" {
		if resp.Response.ActivitySet[0].StatusMessageSimplified == nil {
			resp.Response.ActivitySet[0].StatusMessageSimplified = common.StringPtr("no message")
		}
		return fmt.Errorf("AutoScalingActivity scalingActivityId:%s %s %s", scalingActivityId, *resp.Response.ActivitySet[0].StatusCode, *resp.Response.ActivitySet[0].StatusMessageSimplified)
	}
	return nil
}
