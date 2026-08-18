// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package lightsail provisions VMs on AWS Lightsail using the in-process AWS
// Go SDK (it reads ~/.aws directly, so no aws CLI is required). It implements
// provider.Provider: create an Ubuntu instance, open 22/80/443, attach a static
// IP, and return how to reach it over SSH.
package lightsail

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/provider"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	"github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

const (
	defaultBlueprint = "ubuntu_24_04"
	defaultBundle    = "nano_3_0" // ~$3.50/mo, smallest
	sshUser          = "ubuntu"
)

// Provider provisions VMs on AWS Lightsail.
type Provider struct {
	client *lightsail.Client
	region string
}

// New builds a Lightsail provider for the given region (falls back to the
// region resolved from ~/.aws when region is empty).
func New(ctx context.Context, region string) (*Provider, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	if region == "" {
		region = cfg.Region
	}
	if region == "" {
		return nil, fmt.Errorf("no AWS region set: pass --region or configure one in ~/.aws/config")
	}
	return &Provider{client: lightsail.NewFromConfig(cfg), region: region}, nil
}

// Name returns the provider id.
func (p *Provider) Name() string { return "lightsail" }

// Provision creates a running Ubuntu instance with 22/80/443 open and a static
// IP attached, and returns how to reach it.
func (p *Provider) Provision(ctx context.Context, spec provider.Spec) (provider.Instance, error) {
	bundle := spec.Size
	if bundle == "" {
		bundle = defaultBundle
	}
	az := p.region + "a"
	keyName := spec.Name + "-baryovm"
	sipName := spec.Name + "-ip"

	// 1. Authorize the SSH key (ignore "already exists").
	if _, err := p.client.ImportKeyPair(ctx, &lightsail.ImportKeyPairInput{
		KeyPairName:     aws.String(keyName),
		PublicKeyBase64: aws.String(spec.PublicKey),
	}); err != nil && !alreadyExists(err) {
		return provider.Instance{}, fmt.Errorf("import key pair: %w", err)
	}

	// 2. Create the instance.
	if _, err := p.client.CreateInstances(ctx, &lightsail.CreateInstancesInput{
		InstanceNames:    []string{spec.Name},
		AvailabilityZone: aws.String(az),
		BlueprintId:      aws.String(defaultBlueprint),
		BundleId:         aws.String(bundle),
		KeyPairName:      aws.String(keyName),
	}); err != nil && !alreadyExists(err) {
		return provider.Instance{}, fmt.Errorf("create instance: %w", err)
	}

	// 3. Wait until it is running.
	if err := p.waitRunning(ctx, spec.Name, 3*time.Minute); err != nil {
		return provider.Instance{}, err
	}

	// 4. Open the web + SSH ports.
	if _, err := p.client.PutInstancePublicPorts(ctx, &lightsail.PutInstancePublicPortsInput{
		InstanceName: aws.String(spec.Name),
		PortInfos: []types.PortInfo{
			{FromPort: 22, ToPort: 22, Protocol: types.NetworkProtocolTcp},
			{FromPort: 80, ToPort: 80, Protocol: types.NetworkProtocolTcp},
			{FromPort: 443, ToPort: 443, Protocol: types.NetworkProtocolTcp},
		},
	}); err != nil {
		return provider.Instance{}, fmt.Errorf("open ports: %w", err)
	}

	// 5. Allocate + attach a static IP (ignore "already exists").
	if _, err := p.client.AllocateStaticIp(ctx, &lightsail.AllocateStaticIpInput{
		StaticIpName: aws.String(sipName),
	}); err != nil && !alreadyExists(err) {
		return provider.Instance{}, fmt.Errorf("allocate static ip: %w", err)
	}
	if _, err := p.client.AttachStaticIp(ctx, &lightsail.AttachStaticIpInput{
		StaticIpName: aws.String(sipName),
		InstanceName: aws.String(spec.Name),
	}); err != nil && !alreadyExists(err) {
		return provider.Instance{}, fmt.Errorf("attach static ip: %w", err)
	}

	// 6. Read back the public IP.
	inst, err := p.client.GetInstance(ctx, &lightsail.GetInstanceInput{InstanceName: aws.String(spec.Name)})
	if err != nil {
		return provider.Instance{}, fmt.Errorf("get instance: %w", err)
	}
	return provider.Instance{
		ID:       spec.Name,
		Name:     spec.Name,
		PublicIP: aws.ToString(inst.Instance.PublicIpAddress),
		User:     sshUser,
		Provider: "lightsail",
	}, nil
}

// Destroy tears down the instance, its static IP, and its key pair.
func (p *Provider) Destroy(ctx context.Context, id string) error {
	_, _ = p.client.DetachStaticIp(ctx, &lightsail.DetachStaticIpInput{StaticIpName: aws.String(id + "-ip")})
	_, _ = p.client.ReleaseStaticIp(ctx, &lightsail.ReleaseStaticIpInput{StaticIpName: aws.String(id + "-ip")})
	if _, err := p.client.DeleteInstance(ctx, &lightsail.DeleteInstanceInput{InstanceName: aws.String(id)}); err != nil {
		return fmt.Errorf("delete instance: %w", err)
	}
	_, _ = p.client.DeleteKeyPair(ctx, &lightsail.DeleteKeyPairInput{KeyPairName: aws.String(id + "-baryovm")})
	return nil
}

func (p *Provider) waitRunning(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		st, err := p.client.GetInstanceState(ctx, &lightsail.GetInstanceStateInput{InstanceName: aws.String(name)})
		if err == nil && st.State != nil && aws.ToString(st.State.Name) == "running" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("instance %s did not reach running state within %s", name, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func alreadyExists(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists")
}
