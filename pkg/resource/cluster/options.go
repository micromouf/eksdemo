package cluster

import (
	"fmt"
	"net"
	"strings"

	"github.com/awslabs/eksdemo/pkg/application"
	"github.com/awslabs/eksdemo/pkg/application/autoscaling/karpenter"
	"github.com/awslabs/eksdemo/pkg/application/aws_lb_controller"
	"github.com/awslabs/eksdemo/pkg/application/external_dns"
	"github.com/awslabs/eksdemo/pkg/application/storage/ebs_csi"
	"github.com/awslabs/eksdemo/pkg/aws"
	"github.com/awslabs/eksdemo/pkg/cmd"
	"github.com/awslabs/eksdemo/pkg/resource"
	"github.com/awslabs/eksdemo/pkg/resource/cloudformation_stack"
	"github.com/awslabs/eksdemo/pkg/resource/irsa"
	"github.com/awslabs/eksdemo/pkg/resource/nodegroup"
	"github.com/awslabs/eksdemo/pkg/template"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/spf13/cobra"
)

type ClusterOptions struct {
	resource.CommonOptions
	*nodegroup.NodegroupOptions

	Fargate          bool
	IPv6             bool
	NoRoles          bool
	PrefixAssignment bool
	Private          bool
	VpcCidr          string

	appsForIrsa  []*application.Application
	IrsaTemplate *template.TextTemplate
	IrsaRoles    []*resource.Resource
}

func addOptions(res *resource.Resource) *resource.Resource {
	ngOptions, ngFlags, _ := nodegroup.NewOptions()

	options := &ClusterOptions{
		CommonOptions: resource.CommonOptions{
			ClusterFlagDisabled: true,
			KubernetesVersion:   "1.26",
		},

		NodegroupOptions: ngOptions,
		NoRoles:          false,
		VpcCidr:          "192.168.0.0/16",

		appsForIrsa: []*application.Application{
			aws_lb_controller.NewApp(),
			ebs_csi.NewApp(),
			external_dns.NewApp(),
			karpenter.NewApp(),
		},
		IrsaTemplate: &template.TextTemplate{
			Template: irsa.EksctlTemplate,
		},
	}

	ngOptions.CommonOptions = options.Common()
	ngOptions.DesiredCapacity = 2
	ngOptions.NodegroupName = "main"

	res.Options = options

	flags := cmd.Flags{
		&cmd.StringFlag{
			CommandFlag: cmd.CommandFlag{
				Name:        "version",
				Description: "Kubernetes version",
				Shorthand:   "v",
			},
			Choices: []string{"1.26", "1.25", "1.24", "1.23", "1.22"},
			Option:  &options.KubernetesVersion,
		},
		&cmd.BoolFlag{
			CommandFlag: cmd.CommandFlag{
				Name:        "fargate",
				Description: "create a Fargate profile",
			},
			Option: &options.Fargate,
		},
		&cmd.BoolFlag{
			CommandFlag: cmd.CommandFlag{
				Name:        "ipv6",
				Description: "use IPv6 networking",
			},
			Option: &options.IPv6,
		},
		&cmd.BoolFlag{
			CommandFlag: cmd.CommandFlag{
				Name:        "no-roles",
				Description: "don't create IAM roles",
			},
			Option: &options.NoRoles,
		},
		&cmd.BoolFlag{
			CommandFlag: cmd.CommandFlag{
				Name:        "prefix-assignment",
				Description: "configured VPC CNI for prefix assignment",
			},
			Option: &options.PrefixAssignment,
		},
		&cmd.BoolFlag{
			CommandFlag: cmd.CommandFlag{
				Name:        "private",
				Description: "private cluster (includes ECR, S3, and other VPC endpoints)",
			},
			Option: &options.Private,
		},
		&cmd.StringFlag{
			CommandFlag: cmd.CommandFlag{
				Name:        "vpc-cidr",
				Description: "CIDR to use for EKS Cluster VPC",
				Validate: func(cmd *cobra.Command, args []string) error {
					_, _, err := net.ParseCIDR(options.VpcCidr)
					if err != nil {
						return fmt.Errorf("failed parsing --vpc-cidr, %w", err)
					}
					return nil
				},
			},
			Option: &options.VpcCidr,
		},
	}

	res.CreateFlags = append(ngFlags, flags...)

	return res
}

func (o *ClusterOptions) PreCreate() error {
	o.Account = aws.AccountId()
	o.Partition = aws.Partition()
	o.Region = aws.Region()
	o.NodegroupOptions.KubernetesVersion = o.KubernetesVersion

	// For apps we want to pre-create IRSA for, find the IRSA dependency
	for _, app := range o.appsForIrsa {
		for _, res := range app.Dependencies {
			if res.Name != "irsa" {
				continue
			}
			// Populate the IRSA Resource with Account, Cluster, Namespace, Partition, Region, ServiceAccount
			app.Common().Account = o.Account
			app.Common().ClusterName = o.ClusterName
			app.Common().Region = o.Region
			app.Common().Partition = o.Partition
			app.AssignCommonResourceOptions(res)
			res.SetName(app.Common().ServiceAccount)

			o.IrsaRoles = append(o.IrsaRoles, res)
		}
	}

	return o.NodegroupOptions.PreCreate()
}

func (o *ClusterOptions) PreDelete() error {
	o.Region = aws.Region()

	cloudformationClient := aws.NewCloudformationClient()
	stacks, err := cloudformation_stack.NewGetter(cloudformationClient).GetStacksByCluster(o.ClusterName, "")
	if err != nil {
		return err
	}

	for _, stack := range stacks {
		stackName := awssdk.ToString(stack.StackName)
		if strings.HasPrefix(stackName, "eksdemo-") {
			fmt.Printf("Deleting Cloudformation stack %q\n", stackName)
			err := cloudformationClient.DeleteStack(stackName)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (o *ClusterOptions) SetName(name string) {
	o.ClusterName = name
}