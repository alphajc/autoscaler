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
	"reflect"
	"sync"

	as "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/as/v20180419"
	"k8s.io/klog/v2"

	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
)

// TencentcloudCache is used for caching cluster resources state.
//
// It is needed to:
// - keep track of autoscaled ASGs in the cluster,
// - keep track of instances and which ASG they belong to,
// - limit repetitive Tencentcloud API calls.
//
// Cached resources:
// 1) ASG configuration,
// 2) instance->ASG mapping,
// 3) resource limits (self-imposed quotas),
// 4) instance types.
//
// How it works:
// - asgs (1), resource limits (3) and machine types (4) are only stored in this cache,
// not updated by it.
// - instanceRefToAsgRef (2) is based on registered asgs (1). For each asg, its instances
// are fetched from Tencentcloud API using cloudService.
// - instanceRefToAsgRef (2) is NOT updated automatically when asgs field (1) is updated. Calling
// RegenerateInstancesCache is required to sync it with registered asgs.
type TencentcloudCache struct {
	cacheMutex sync.RWMutex

	// Cache content.
	asgs                     map[TencentcloudRef]Asg
	instanceRefToAsgRef      map[TencentcloudRef]TencentcloudRef
	instancesFromUnknownAsgs map[TencentcloudRef]struct{}
	asgTargetSizeCache       map[TencentcloudRef]int64
	instanceTypeCache        map[TencentcloudRef]string
	resourceLimiter          *cloudprovider.ResourceLimiter

	// Service used to refresh cache.
	cloudService CloudService
}

// NewTencentcloudCache create a empty TencentcloudCache
func NewTencentcloudCache(service CloudService) *TencentcloudCache {
	registry := &TencentcloudCache{
		cloudService:             service,
		asgs:                     make(map[TencentcloudRef]Asg),
		instanceRefToAsgRef:      make(map[TencentcloudRef]TencentcloudRef),
		instancesFromUnknownAsgs: make(map[TencentcloudRef]struct{}),
		asgTargetSizeCache:       make(map[TencentcloudRef]int64),
		instanceTypeCache:        make(map[TencentcloudRef]string),
	}

	return registry
}

// RegisterAsg registers asg in Tencentcloud Manager.
func (tc *TencentcloudCache) RegisterAsg(newAsg Asg) bool {
	tc.cacheMutex.Lock()
	defer tc.cacheMutex.Unlock()

	oldAsg, found := tc.asgs[newAsg.TencentcloudRef()]
	if found {
		if !reflect.DeepEqual(oldAsg, newAsg) {
			tc.asgs[newAsg.TencentcloudRef()] = newAsg
			klog.V(4).Infof("Updated Asg %s", newAsg.TencentcloudRef().ID)
			return true
		}
		return false
	}

	klog.V(1).Infof("Registering %s", newAsg.TencentcloudRef().ID)
	tc.asgs[newAsg.TencentcloudRef()] = newAsg
	return true
}

// UnregisterAsg returns true if the node group has been removed, and false if it was already missing from cache.
func (tc *TencentcloudCache) UnregisterAsg(toBeRemoved Asg) bool {
	tc.cacheMutex.Lock()
	defer tc.cacheMutex.Unlock()

	_, found := tc.asgs[toBeRemoved.TencentcloudRef()]
	if found {
		klog.V(1).Infof("Unregistered Asg %s", toBeRemoved.TencentcloudRef().String())
		delete(tc.asgs, toBeRemoved.TencentcloudRef())
		tc.removeInstancesForAsgs(toBeRemoved.TencentcloudRef())
		return true
	}
	return false
}

// FindForInstance returns Asg of the given Instance
func (tc *TencentcloudCache) FindForInstance(instanceRef TencentcloudRef) (Asg, error) {
	tc.cacheMutex.Lock()
	defer tc.cacheMutex.Unlock()

	if asgRef, found := tc.instanceRefToAsgRef[instanceRef]; found {
		asg, found := tc.getAsgNoLock(asgRef)
		if !found {
			return nil, fmt.Errorf("instance %+v belongs to unregistered asg %+v", instanceRef, asgRef)
		}
		return asg, nil
	} else if _, found := tc.instancesFromUnknownAsgs[instanceRef]; found {
		return nil, nil
	}

	asgRef, err := tc.cloudService.GetAsgRefByInstanceRef(instanceRef)
	if err != nil {
		return nil, err
	}

	tc.instanceRefToAsgRef[instanceRef] = asgRef
	asg, found := tc.getAsgNoLock(asgRef)
	if !found {
		return nil, fmt.Errorf("instance %+v belongs to unregistered asg %+v", instanceRef, asgRef)
	}
	return asg, nil
}

// GetAsgs returns a copy of asgs list.
func (tc *TencentcloudCache) GetAsgs() []Asg {
	tc.cacheMutex.Lock()
	defer tc.cacheMutex.Unlock()

	asgs := make([]Asg, 0, len(tc.asgs))
	for _, asg := range tc.asgs {
		asgs = append(asgs, asg)
	}
	return asgs
}

// GetAsgs returns a copy of asgs list.
func (tc *TencentcloudCache) getAsgRefs() []TencentcloudRef {
	asgRefs := make([]TencentcloudRef, 0, len(tc.asgs))
	for asgRef := range tc.asgs {
		asgRefs = append(asgRefs, asgRef)
	}
	return asgRefs
}

// GetInstanceType returns asg instanceType
func (tc *TencentcloudCache) GetInstanceType(ref TencentcloudRef) string {
	tc.cacheMutex.RLock()
	defer tc.cacheMutex.RUnlock()

	return tc.instanceTypeCache[ref]
}

// GetAsgTargetSize returns the cached targetSize for a TencentcloudRef
func (tc *TencentcloudCache) GetAsgTargetSize(ref TencentcloudRef) (int64, bool) {
	tc.cacheMutex.RLock()
	defer tc.cacheMutex.RUnlock()

	size, found := tc.asgTargetSizeCache[ref]
	if found {
		klog.V(5).Infof("Target size cache hit for %s", ref)
	}
	return size, found
}

// SetAsgTargetSize sets targetSize for a TencentcloudRef
func (tc *TencentcloudCache) SetAsgTargetSize(ref TencentcloudRef, size int64) {
	tc.cacheMutex.Lock()
	defer tc.cacheMutex.Unlock()

	tc.asgTargetSizeCache[ref] = size
}

// InvalidateAsgTargetSize clears the target size cache
func (tc *TencentcloudCache) InvalidateAsgTargetSize(ref TencentcloudRef) {
	tc.cacheMutex.Lock()
	defer tc.cacheMutex.Unlock()

	if _, found := tc.asgTargetSizeCache[ref]; found {
		klog.V(5).Infof("Target size cache invalidated for %s", ref)
		delete(tc.asgTargetSizeCache, ref)
	}
}

// InvalidateAllAsgTargetSizes clears the target size cache
func (tc *TencentcloudCache) InvalidateAllAsgTargetSizes() {
	tc.cacheMutex.Lock()
	defer tc.cacheMutex.Unlock()

	klog.V(5).Infof("Target size cache invalidated")
	tc.asgTargetSizeCache = map[TencentcloudRef]int64{}
}

func (tc *TencentcloudCache) removeInstancesForAsgs(asgRef TencentcloudRef) {
	for instanceRef, instanceAsgRef := range tc.instanceRefToAsgRef {
		if asgRef == instanceAsgRef {
			delete(tc.instanceRefToAsgRef, instanceRef)
		}
	}
}

func (tc *TencentcloudCache) getAsgNoLock(asgRef TencentcloudRef) (asg Asg, found bool) {
	asg, found = tc.asgs[asgRef]
	return
}

// RegenerateInstanceCacheForAsg triggers instances cache regeneration for single ASG under lock.
func (tc *TencentcloudCache) RegenerateInstanceCacheForAsg(asgRef TencentcloudRef) error {
	tc.cacheMutex.Lock()
	defer tc.cacheMutex.Unlock()
	return tc.regenerateInstanceCacheForAsgNoLock(asgRef)
}

func (tc *TencentcloudCache) regenerateInstanceCacheForAsgNoLock(asgRef TencentcloudRef) error {
	klog.V(4).Infof("Regenerating ASG information for %s", asgRef.String())

	// cleanup old entries
	tc.removeInstancesForAsgs(asgRef)

	instances, err := tc.cloudService.FetchAsgInstances(asgRef)
	if err != nil {
		klog.V(4).Infof("Failed ASG info request for %s: %v", asgRef.String(), err)
		return err
	}
	for _, instance := range instances {
		instanceRef, err := TencentcloudRefFromProviderId(instance.Id)
		if err != nil {
			return err
		}
		tc.instanceRefToAsgRef[instanceRef] = asgRef
	}
	return nil
}

// RegenerateInstancesCache triggers instances cache regeneration under lock.
func (tc *TencentcloudCache) RegenerateInstancesCache() error {
	tc.cacheMutex.Lock()
	defer tc.cacheMutex.Unlock()

	tc.instanceRefToAsgRef = make(map[TencentcloudRef]TencentcloudRef)
	tc.instancesFromUnknownAsgs = make(map[TencentcloudRef]struct{})
	for _, asgRef := range tc.getAsgRefs() {
		err := tc.regenerateInstanceCacheForAsgNoLock(asgRef)
		if err != nil {
			return err
		}
	}
	return nil
}

// SetResourceLimiter sets resource limiter.
func (tc *TencentcloudCache) SetResourceLimiter(resourceLimiter *cloudprovider.ResourceLimiter) {
	tc.cacheMutex.Lock()
	defer tc.cacheMutex.Unlock()

	tc.resourceLimiter = resourceLimiter
}

// GetResourceLimiter returns resource limiter.
func (tc *TencentcloudCache) GetResourceLimiter() (*cloudprovider.ResourceLimiter, error) {
	tc.cacheMutex.RLock()
	defer tc.cacheMutex.RUnlock()

	return tc.resourceLimiter, nil
}

// RegenerateAutoScalingGroupCache add some tencentcloud asg property
func (tc *TencentcloudCache) RegenerateAutoScalingGroupCache() error {
	tc.cacheMutex.Lock()
	defer tc.cacheMutex.Unlock()

	asgIDs := make([]string, 0)

	for ref := range tc.asgs {
		asgIDs = append(asgIDs, ref.ID)
	}

	tcAsgs, err := tc.cloudService.GetAutoScalingGroups(asgIDs)
	if err != nil {
		return err
	}

	asgMap := make(map[string]as.AutoScalingGroup)
	ascMap := make(map[string]as.LaunchConfiguration)
	asc2Asg := make(map[string]string)
	ascs := make([]string, 0)
	for _, tcAsg := range tcAsgs {
		if tcAsg.AutoScalingGroupId == nil || tcAsg.ServiceSettings == nil || tcAsg.ServiceSettings.ScalingMode == nil || tcAsg.LaunchConfigurationId == nil {
			return fmt.Errorf("invalid autoscaling group")
		}
		asgMap[*tcAsg.AutoScalingGroupId] = tcAsg
		ascs = append(ascs, *tcAsg.LaunchConfigurationId)
		asc2Asg[*tcAsg.LaunchConfigurationId] = *tcAsg.AutoScalingGroupId
	}

	tcAscs, err := tc.cloudService.GetAutoscalingConfigs(ascs)
	if err != nil {
		return err
	}

	for _, tcAsc := range tcAscs {
		if tcAsc.InstanceType == nil {
			return fmt.Errorf("invalid launch configuration")
		}
		ascMap[asc2Asg[*tcAsc.LaunchConfigurationId]] = tcAsc
	}

	// set asg
	for _, ref := range tc.getAsgRefs() {
		asg := tc.asgs[ref]
		asg.SetScalingType(*asgMap[ref.ID].ServiceSettings.ScalingMode)
		tc.asgs[ref] = asg
		if asgMap[ref.ID].DesiredCapacity == nil {
			klog.Warningf("%s has a invalid desired capacity value", ref.String())
			continue
		}
		tc.asgTargetSizeCache[ref] = *asgMap[ref.ID].DesiredCapacity
		tc.instanceTypeCache[ref] = *ascMap[ref.ID].InstanceType
	}

	return nil
}
