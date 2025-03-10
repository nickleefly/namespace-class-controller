# NamespaceClass Controller

The NamespaceClass controller allows Kubernetes administrators to define standard classes of namespaces with predefined resources that are automatically created and managed.

## Overview

When a namespace is created with a specific NamespaceClass label, the controller automatically creates and manages the resources defined in the NamespaceClass. This makes it easy to standardize namespace configurations across your cluster.

## Features

- **Resource Creation**: Automatically creates resources (NetworkPolicies, LimitRanges, etc.) in namespaces based on their class
- **Resource Management**: Updates resources when the NamespaceClass changes
- **Class Switching**: Supports changing a namespace's class, automatically managing the transition
- **Resource Cleanup**: Automatically removes managed resources when a class is removed or the namespace is deleted


## 1, Build and Load the Docker Image

```
docker build -t namespaceclass-controller:latest .
```

## 2, Load the image into Minikube

```
minikube image load namespaceclass-controller:latest
```

## Start minikube

```
minikube start
```

## Apply CRD

```
kubectl apply -f config/crd/namespaceclasses.yaml
```

## Deploy the Controller

```
kubectl apply -f config/deploy/deployment.yaml
kubectl apply -f config/rbac/role.yaml
kubectl apply -f config/rbac/rolebinding.yaml
kubectl apply -f config/rbac/serviceaccount.yaml
```

## Apply the Examples

```
kubectl apply -f examples/public-network.yaml
kubectl apply -f examples/internal-network.yaml
kubectl apply -f examples/web-portal.yaml
kubectl describe namespaceclass public-network -o yaml
kubectl describe namespaceclass internal-network -o yaml
```

## Verify the Creation of Resources :

```
kubectl describe namespace web-portal -o yaml
```

## Testing the Controller

### 1, Switch Classes:
Edit the web-portal namespace to change its label to internal-network:
bash

```
kubectl edit namespace web-portal
```

Change the label to `namespaceclass.akuity.io/name: internal-network`. Verify the resources update
```
kubectl describe namespace web-portal
```

### 2, Update a NamespaceClass
Modify public-network.yaml (e.g., change a NetworkPolicy rule) and reapply:

```
kubectl get namespaceclasses public-network -o yaml
kubectl describe namespaceclasses public-network -o yaml
```

