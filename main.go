package main

import (
	"fmt"
	"os"

	"github.com/pulumi/pulumi-aws/sdk/v5/go/aws/acm"
	"github.com/pulumi/pulumi-aws/sdk/v5/go/aws/cloudfront"
	"github.com/pulumi/pulumi-aws/sdk/v5/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v5/go/aws/route53"
	"github.com/pulumi/pulumi-aws/sdk/v5/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Structs to store a data.
type Project struct {
	name string
}

type Environment struct {
	name string
}

type Site struct {
	dir string
}

type Domain struct {
	name string
}

type Tags struct {
	tags map[string]string
}

type WebBucket struct {
	name          string
	indexDocument string
	errorDocument string
}

func main() {

	pulumi.Run(func(ctx *pulumi.Context) error {

		// Project Variables
		// -----------------
		project := Project{
			name: "stratusLabs",
		}

		environment := Environment{
			name: "dev",
		}

		site := Site{
			dir: "./www/_site",
		}

		domain := Domain{
			name: "stratuslabs.net",
		}

		tags := Tags{
			tags: map[string]string{
				"project":     project.name,
				"environment": environment.name,
			},
		}

		var priceClass string
		switch environment.name {
		case "dev":
			priceClass = "PriceClass_100"
		case "prod":
			priceClass = "PriceClass_All"
		default:
			priceClass = "PriceClass_100"
		}

		wb := WebBucket{
			name:          fmt.Sprintf("www.%s", domain.name),
			indexDocument: "index.html",
			errorDocument: "error.html",
		}

		// Website Files
		// -------------
		// Load the file to transfer to the websites S3 bucket.
		files, err := os.ReadDir(fmt.Sprintf("%s/", site.dir))
		if err != nil {
			return err
		}

		// Domain Name
		// -----------
		// Load the instance of the domain name that was purchased for the website.
		domainZone, err := route53.LookupZone(ctx, &route53.LookupZoneArgs{
			Name: pulumi.StringRef(domain.name),
		}, nil)
		if err != nil {
			return err
		}

		// S3
		// --
		// Create an S3 bucket and enalbe Web Hosting in order to host the website.
		bucket, err := s3.NewBucket(ctx, fmt.Sprintf("%sBucket", project.name), &s3.BucketArgs{
			Bucket: pulumi.String(wb.name),
			Website: &s3.BucketWebsiteArgs{
				IndexDocument: pulumi.String(wb.indexDocument),
				ErrorDocument: pulumi.String(wb.errorDocument),
			},
			Tags: pulumi.ToStringMap(tags.tags),
		})
		if err != nil {
			return err
		}

		// Make bucket private. This blocks all access directly to the bucket.
		// Access will be permitted for CloudFront to the bucket via a bucket policy.
		_, err = s3.NewBucketPublicAccessBlock(ctx, fmt.Sprintf("%sBucketNoPublic", project.name), &s3.BucketPublicAccessBlockArgs{
			Bucket:                bucket.ID(),
			BlockPublicAcls:       pulumi.Bool(true),
			BlockPublicPolicy:     pulumi.Bool(true),
			IgnorePublicAcls:      pulumi.Bool(true),
			RestrictPublicBuckets: pulumi.Bool(true),
		})
		if err != nil {
			return err
		}

		// Upload the website files to the bucket.
		for _, file := range files {
			_, err = s3.NewBucketObject(ctx, file.Name(), &s3.BucketObjectArgs{
				Key:         pulumi.String(file.Name()),
				Bucket:      bucket.ID(),
				Source:      pulumi.NewFileAsset(fmt.Sprintf("%s/%s", site.dir, file.Name())),
				ContentType: pulumi.String("text/html"),
				Tags:        pulumi.ToStringMap(tags.tags),
			})
			if err != nil {
				return err
			}
		}

		// Certificate Manager
		// -------------------
		// Create a Public Certificate that will be used in the CloudFront distribution
		// to enable TLS connections to the website.

		certificate, err := acm.NewCertificate(ctx, fmt.Sprintf("%sCert", project.name), &acm.CertificateArgs{
			DomainName:       pulumi.String(domain.name),
			ValidationMethod: pulumi.String("DNS"),
			SubjectAlternativeNames: pulumi.StringArray{
				pulumi.String(fmt.Sprintf("www.%s", domain.name)),
			},
			Tags: pulumi.ToStringMap(tags.tags),
		})
		if err != nil {
			return err
		}

		// Add CNAME records to Route53. This is used to validate that we own
		// the domain we are requesting certificates for.
		for i := 0; i <= 1; i++ {
			_, err := route53.NewRecord(ctx, fmt.Sprintf("%sCname%d", project.name, i), &route53.RecordArgs{
				ZoneId: pulumi.String(domainZone.Id),
				Name:   certificate.DomainValidationOptions.Index(pulumi.Int(i)).ResourceRecordName().Elem(),
				Type:   pulumi.String("CNAME"),
				Ttl:    pulumi.Int(60),
				Records: pulumi.StringArray{
					certificate.DomainValidationOptions.Index(pulumi.Int(i)).ResourceRecordValue().Elem(),
				},
			})
			if err != nil {
				return err
			}
		}

		// CloudFront
		// ----------
		// Create a CloudFront Origin Access Identity.
		// This is used to attach the CloudFront Distribution to an S3 bucket.
		originAccessId, err := cloudfront.NewOriginAccessIdentity(ctx, fmt.Sprintf("%sOriginAccessId", project.name), &cloudfront.OriginAccessIdentityArgs{
			Comment: pulumi.String(project.name),
		})
		if err != nil {
			return err
		}

		// Create a CloudFront Distribution
		cloudFrontDist, err := cloudfront.NewDistribution(ctx, fmt.Sprintf("%sDistribution", project.name), &cloudfront.DistributionArgs{
			Origins: cloudfront.DistributionOriginArray{
				&cloudfront.DistributionOriginArgs{
					DomainName: bucket.BucketRegionalDomainName,
					OriginId:   bucket.ID(),
					S3OriginConfig: &cloudfront.DistributionOriginS3OriginConfigArgs{
						OriginAccessIdentity: originAccessId.CloudfrontAccessIdentityPath,
					},
				},
			},
			Enabled:           pulumi.Bool(true),
			HttpVersion:       pulumi.String("http2and3"),
			IsIpv6Enabled:     pulumi.Bool(true),
			DefaultRootObject: pulumi.String("index.html"),
			// No logging config at the moment, this will be added as an
			// option in the future
			// LoggingConfig: &cloudfront.DistributionLoggingConfigArgs{
			// 	IncludeCookies: pulumi.Bool(false),
			// 	Bucket:         pulumi.String("mylogs.s3.amazonaws.com"),
			// 	Prefix:         pulumi.String("myprefix"),
			// },
			Aliases: pulumi.StringArray{
				pulumi.String(domain.name),
				pulumi.String(fmt.Sprintf("www.%s", domain.name)),
			},
			DefaultCacheBehavior: &cloudfront.DistributionDefaultCacheBehaviorArgs{
				AllowedMethods: pulumi.StringArray{
					pulumi.String("GET"),
					pulumi.String("HEAD"),
				},
				CachedMethods: pulumi.StringArray{
					pulumi.String("GET"),
					pulumi.String("HEAD"),
				},
				TargetOriginId: bucket.ID(),
				ForwardedValues: &cloudfront.DistributionDefaultCacheBehaviorForwardedValuesArgs{
					QueryString: pulumi.Bool(false),
					Cookies: &cloudfront.DistributionDefaultCacheBehaviorForwardedValuesCookiesArgs{
						Forward: pulumi.String("none"),
					},
				},
				ViewerProtocolPolicy: pulumi.String("redirect-to-https"),
				MinTtl:               pulumi.Int(0),
				DefaultTtl:           pulumi.Int(3600),
				MaxTtl:               pulumi.Int(86400),
			},
			PriceClass: pulumi.String(priceClass),
			Restrictions: &cloudfront.DistributionRestrictionsArgs{
				GeoRestriction: &cloudfront.DistributionRestrictionsGeoRestrictionArgs{
					// Update this section to enable Geo-Restrictions.
					RestrictionType: pulumi.String("none"),
					// Locations: pulumi.StringArray{
					// 	pulumi.String("US"),
					// 	pulumi.String("CA"),
					// 	pulumi.String("GB"),
					// 	pulumi.String("DE"),
					// },
				},
			},
			ViewerCertificate: &cloudfront.DistributionViewerCertificateArgs{
				CloudfrontDefaultCertificate: pulumi.Bool(false),
				AcmCertificateArn:            certificate.Arn,
				SslSupportMethod:             pulumi.String("sni-only"),
				MinimumProtocolVersion:       pulumi.String("TLSv1.2_2021"),
			},
			Tags: pulumi.ToStringMap(tags.tags),
		})
		if err != nil {
			return err
		}

		// Create DNS records for the website.
		// The A/AAAA records are alias records that point to the
		// CloudFront distribution. Records are created for both
		// the bare domain `example.domain` and the `www.example.domain`
		for _, record := range []string{"A", "AAAA"} {
			_, err := route53.NewRecord(ctx, fmt.Sprintf("%s%s", project.name, record), &route53.RecordArgs{
				ZoneId: pulumi.String(domainZone.Id),
				Name:   pulumi.String(domain.name),
				Type:   pulumi.String(record),
				Aliases: route53.RecordAliasArray{
					&route53.RecordAliasArgs{
						Name:                 cloudFrontDist.DomainName,
						ZoneId:               cloudFrontDist.HostedZoneId,
						EvaluateTargetHealth: pulumi.Bool(true),
					},
				},
			})
			if err != nil {
				return err
			}
			_, err = route53.NewRecord(ctx, fmt.Sprintf("www%s%s", project.name, record), &route53.RecordArgs{
				ZoneId: pulumi.String(domainZone.Id),
				Name:   pulumi.String(fmt.Sprintf("www.%s", domain.name)),
				Type:   pulumi.String(record),
				Aliases: route53.RecordAliasArray{
					&route53.RecordAliasArgs{
						Name:                 cloudFrontDist.DomainName,
						ZoneId:               cloudFrontDist.HostedZoneId,
						EvaluateTargetHealth: pulumi.Bool(true),
					},
				},
			})
			if err != nil {
				return err
			}
		}

		// S3
		// --
		// Create a bucket policy that allows access to the bucket
		// only from the CloudFront distribution.
		bucketPolicy := iam.GetPolicyDocumentOutput(ctx, iam.GetPolicyDocumentOutputArgs{
			PolicyId: pulumi.String("PolicyForCloudFrontPrivateContent"),
			Version:  pulumi.String("2008-10-17"),
			Statements: iam.GetPolicyDocumentStatementArray{
				&iam.GetPolicyDocumentStatementArgs{
					Sid: pulumi.String("1"),
					Principals: iam.GetPolicyDocumentStatementPrincipalArray{
						&iam.GetPolicyDocumentStatementPrincipalArgs{
							Type: pulumi.String("AWS"),
							Identifiers: pulumi.StringArray{
								originAccessId.IamArn,
							},
						},
					},
					Actions: pulumi.StringArray{
						pulumi.String("s3:GetObject"),
					},
					Resources: pulumi.StringArray{
						pulumi.Sprintf("%v/*", bucket.Arn),
					},
				},
			},
		}, nil)

		// Attach the bucket policy to the S3 Bucket.
		_, err = s3.NewBucketPolicy(ctx, fmt.Sprintf("%sBucketPolicy", domain.name), &s3.BucketPolicyArgs{
			Bucket: bucket.ID(),
			Policy: bucketPolicy.ApplyT(func(bucketPolicy iam.GetPolicyDocumentResult) (string, error) {
				return bucketPolicy.Json, nil
			}).(pulumi.StringOutput),
		})
		if err != nil {
			return err
		}

		// Exports will be shown as outputs to the terminal.
		ctx.Export("bucketName", bucket.ID())
		ctx.Export("cloudFrontDist", cloudFrontDist.ID())
		return nil
	})
}
