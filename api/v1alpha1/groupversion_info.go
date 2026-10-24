/*
Copyright The kubevirt-drs Authors.

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

// Package v1alpha1 contains the drs.zhanwei.ai/v1alpha1 API: DRSPolicy (the one brain of a cluster)
// and DRSRecommendation (one proposed or executed live migration, VMware's drmdump entry).
// It depends on k8s.io/apimachinery only, so other projects can import the types cheaply.
// +kubebuilder:object:generate=true
// +groupName=drs.zhanwei.ai
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// Group is the API group of every kind in this package.
	Group = "drs.zhanwei.ai"
	// Version is the API version of every kind in this package.
	Version = "v1alpha1"
)

var (
	// GroupVersion is the group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: Group, Version: Version}

	// SchemeBuilder collects the functions that add this group-version to a scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// Resource returns a GroupResource for the given plural resource name.
func Resource(resource string) schema.GroupResource {
	return GroupVersion.WithResource(resource).GroupResource()
}

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&DRSPolicy{}, &DRSPolicyList{},
		&DRSRecommendation{}, &DRSRecommendationList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
