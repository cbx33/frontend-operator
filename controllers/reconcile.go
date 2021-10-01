package controllers

import (
	"context"
	"encoding/json"
	"fmt"

	crd "github.com/RedHatInsights/frontend-operator/api/v1alpha1"
	resCache "github.com/RedHatInsights/rhc-osdk-utils/resource_cache"
	"github.com/RedHatInsights/rhc-osdk-utils/utils"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func runReconciliation(context context.Context, pClient client.Client, frontend *crd.Frontend, cache *resCache.ObjectCache) error {
	if err := createConfigConfigMap(context, pClient, frontend, cache); err != nil {
		return err
	}

	if err := createFrontendDeployment(context, pClient, frontend, cache); err != nil {
		return err
	}

	if err := createFrontendService(frontend, cache); err != nil {
		return err
	}

	if err := createFrontendIngress(frontend, cache); err != nil {
		return err
	}

	return nil
}

func createFrontendDeployment(context context.Context, pClient client.Client, frontend *crd.Frontend, cache *resCache.ObjectCache) error {
	sso, err := getSSO(context, pClient, frontend, cache)

	if err != nil {
		return err
	}

	// Create new empty struct
	d := &apps.Deployment{}

	// Define name of resource
	nn := types.NamespacedName{
		Name:      frontend.Name,
		Namespace: frontend.Namespace,
	}

	// Create object in cache (will populate cache if exists)
	if err := cache.Create(CoreDeployment, nn, d); err != nil {
		return err
	}

	// Label with the right labels
	labels := frontend.GetLabels()

	labeler := utils.GetCustomLabeler(labels, nn, frontend)
	labeler(d)

	// Modify the obejct to set the things we care about
	d.Spec.Template.Spec.Containers = []v1.Container{{
		Name:  "fe-image",
		Image: frontend.Spec.Image,
		Ports: []v1.ContainerPort{{
			Name:          "web",
			ContainerPort: 80,
			Protocol:      "TCP",
		}},
		VolumeMounts: []v1.VolumeMount{{
			Name:      "config",
			MountPath: "/usr/share/nginx/html/chrome",
		}},
		Env: []v1.EnvVar{{
			Name:  "SSO_URL",
			Value: sso,
		}},
	}}

	d.Spec.Template.Spec.Volumes = []v1.Volume{}
	d.Spec.Template.Spec.Volumes = append(d.Spec.Template.Spec.Volumes, v1.Volume{
		Name: "config",
		VolumeSource: v1.VolumeSource{
			ConfigMap: &v1.ConfigMapVolumeSource{
				LocalObjectReference: v1.LocalObjectReference{
					Name: frontend.Spec.EnvName,
				},
			},
		},
	})

	d.Spec.Template.ObjectMeta.Labels = labels

	d.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}

	// Inform the cache that our updates are complete
	if err := cache.Update(CoreDeployment, d); err != nil {
		return err
	}

	return nil
}

//Will need to create a service resource ident in provider like CoreDeployment
func createFrontendService(frontend *crd.Frontend, cache *resCache.ObjectCache) error {
	// Create empty service
	s := &v1.Service{}

	// Define name of resource
	nn := types.NamespacedName{
		Name:      frontend.Name,
		Namespace: frontend.Namespace,
	}

	// Create object in cache (will populate cache if exists)
	if err := cache.Create(CoreService, nn, s); err != nil {
		return err
	}

	servicePorts := []v1.ServicePort{}

	appProtocol := "http"

	labels := make(map[string]string)
	labels["frontend"] = frontend.Name
	labeler := utils.GetCustomLabeler(labels, nn, frontend)
	labeler(s)
	// We should also set owner reference to the pod

	port := v1.ServicePort{
		Name:        "public",
		Port:        8000,
		TargetPort:  intstr.FromInt(80),
		Protocol:    "TCP",
		AppProtocol: &appProtocol,
	}

	servicePorts = append(servicePorts, port)
	s.Spec.Selector = labels

	utils.MakeService(s, nn, labels, servicePorts, frontend, false)

	// Inform the cache that our updates are complete
	if err := cache.Update(CoreService, s); err != nil {
		return err
	}
	return nil
}

func createFrontendIngress(frontend *crd.Frontend, cache *resCache.ObjectCache) error {
	netobj := &networking.Ingress{}

	nn := types.NamespacedName{
		Name:      frontend.Name,
		Namespace: frontend.Namespace,
	}

	if err := cache.Create(WebIngress, nn, netobj); err != nil {
		return err
	}

	labels := frontend.GetLabels()
	labler := utils.GetCustomLabeler(labels, nn, frontend)
	labler(netobj)

	annotations := netobj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	annotations["kubernetes.io/ingress.class"] = "nginx"

	netobj.SetAnnotations(annotations)

	frontendPath := frontend.Spec.Frontend.Paths
	defaultPath := fmt.Sprintf("/apps/%s", frontend.Name)
	defaultBetaPath := fmt.Sprintf("/beta/apps/%s", frontend.Name)

	if !frontend.Spec.Frontend.HasPath(defaultPath) {
		frontendPath = append(frontendPath, defaultPath)
	}

	if !frontend.Spec.Frontend.HasPath(defaultBetaPath) {
		frontendPath = append(frontendPath, defaultBetaPath)
	}

	prefixType := "Prefix"

	var ingressPapths []networking.HTTPIngressPath
	for _, a := range frontendPath {
		newPath := networking.HTTPIngressPath{
			Path:     a,
			PathType: (*networking.PathType)(&prefixType),
			Backend: networking.IngressBackend{
				Service: &networking.IngressServiceBackend{
					Name: nn.Name,
					Port: networking.ServiceBackendPort{
						Number: 8000,
					},
				},
			},
		}
		ingressPapths = append(ingressPapths, newPath)
	}

	// we need to add /api fallback here as well

	netobj.Spec = networking.IngressSpec{
		Rules: []networking.IngressRule{
			{
				Host: frontend.Spec.EnvName,
				IngressRuleValue: networking.IngressRuleValue{
					HTTP: &networking.HTTPIngressRuleValue{
						Paths: ingressPapths,
					},
				},
			},
		},
	}

	if err := cache.Update(WebIngress, netobj); err != nil {
		return err
	}

	return nil
}

func createConfigConfigMap(ctx context.Context, pClient client.Client, frontend *crd.Frontend, cache *resCache.ObjectCache) error {
	// Will need to interact directly with the client here, and not the cache because
	// we need to read ALL the Frontend CRDs in the Env that we care about

	frontendList := &crd.FrontendList{}

	if err := pClient.List(ctx, frontendList, client.MatchingFields{"spec.envName": frontend.Spec.EnvName}); err != nil {
		return err
	}

	cacheMap := make(map[string]crd.Frontend)
	for _, frontend := range frontendList.Items {
		if frontend.Spec.NavItem == nil {
			continue
		}
		cacheMap[frontend.Name] = frontend
	}

	bundleList := &crd.BundleList{}

	if err := pClient.List(ctx, bundleList, client.MatchingFields{"spec.envName": frontend.Spec.EnvName}); err != nil {
		return err
	}

	cfgMap := &v1.ConfigMap{}

	nn := types.NamespacedName{
		Name:      frontend.Spec.EnvName,
		Namespace: frontend.Namespace,
	}

	if err := cache.Create(CoreConfig, nn, cfgMap); err != nil {
		return err
	}

	labels := frontend.GetLabels()
	labler := utils.GetCustomLabeler(labels, nn, frontend)
	labler(cfgMap)

	cfgMap.Data = map[string]string{}

	for _, bundle := range bundleList.Items {
		if bundle.CustomNav != nil {
			newBundleObject := bundle.CustomNav

			jsonData, err := json.Marshal(newBundleObject)
			if err != nil {
				return err
			}

			cfgMap.Data[fmt.Sprintf("%s.json", bundle.Name)] = string(jsonData)
		} else {
			newBundleObject := crd.ComputedBundle{
				ID:       bundle.Spec.ID,
				Title:    bundle.Spec.Title,
				NavItems: []crd.BundleNavItem{},
			}

			bundleCacheMap := make(map[string]crd.BundleNavItem)
			for _, extraItem := range bundle.Spec.ExtraNavItems {
				bundleCacheMap[extraItem.Name] = extraItem.NavItem
			}

			for _, app := range bundle.Spec.AppList {
				if retrievedFrontend, ok := cacheMap[app]; ok {
					newBundleObject.NavItems = append(newBundleObject.NavItems, *retrievedFrontend.Spec.NavItem)
				}
				if bundleNavItem, ok := bundleCacheMap[app]; ok {
					newBundleObject.NavItems = append(newBundleObject.NavItems, bundleNavItem)
				}
			}

			jsonData, err := json.Marshal(newBundleObject)
			if err != nil {
				return err
			}

			cfgMap.Data[fmt.Sprintf("%s.json", bundle.Name)] = string(jsonData)
		}
	}

	fedModules := make(map[string]crd.FedModule)

	for _, app := range frontendList.Items {
		fedModules[app.GetName()] = app.Spec.Module
	}

	jsonData, err := json.Marshal(fedModules)
	if err != nil {
		return err
	}

	cfgMap.Data["fed-modules.json"] = string(jsonData)

	if err := cache.Update(CoreConfig, cfgMap); err != nil {
		return err
	}

	return nil
}

func getSSO(ctx context.Context, pClient client.Client, frontend *crd.Frontend, cache *resCache.ObjectCache) (string, error) {
	fe := &crd.FrontendEnvironment{}
	if err := pClient.Get(ctx, types.NamespacedName{Name: frontend.Spec.EnvName}, fe); err != nil {
		return "", err
	}

	return fe.Spec.SSO, nil
}