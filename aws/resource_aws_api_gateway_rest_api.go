package aws

import (
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/apigateway"
	"github.com/hashicorp/errwrap"
	"github.com/hashicorp/terraform/helper/resource"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/helper/validation"
)

func resourceAwsApiGatewayRestApi() *schema.Resource {
	return &schema.Resource{
		Create: resourceAwsApiGatewayRestApiCreate,
		Read:   resourceAwsApiGatewayRestApiRead,
		Update: resourceAwsApiGatewayRestApiUpdate,
		Delete: resourceAwsApiGatewayRestApiDelete,

		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
			},

			"description": {
				Type:     schema.TypeString,
				Optional: true,
			},

			"policy": {
				Type:             schema.TypeString,
				Optional:         true,
				ValidateFunc:     validateJsonString,
				DiffSuppressFunc: suppressEquivalentAwsPolicyDiffs,
			},

			"binary_media_types": {
				Type:     schema.TypeList,
				Optional: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},

			"body": {
				Type:     schema.TypeString,
				Optional: true,
			},

			"minimum_compression_size": {
				Type:         schema.TypeInt,
				Optional:     true,
				Default:      -1,
				ValidateFunc: validateIntegerInRange(-1, 10485760),
			},

			"root_resource_id": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"created_date": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"execution_arn": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"endpoint_configuration": {
				Type:     schema.TypeList,
				Optional: true,
				Computed: true,
				MinItems: 1,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"types": {
							Type:     schema.TypeList,
							Required: true,
							MinItems: 1,
							MaxItems: 1,
							Elem: &schema.Schema{
								Type: schema.TypeString,
								ValidateFunc: validation.StringInSlice([]string{
									apigateway.EndpointTypeEdge,
									apigateway.EndpointTypeRegional,
								}, false),
							},
						},
					},
				},
			},
		},
	}
}

func resourceAwsApiGatewayRestApiCreate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).apigateway
	log.Printf("[DEBUG] Creating API Gateway")

	var description *string
	if d.Get("description").(string) != "" {
		description = aws.String(d.Get("description").(string))
	}

	params := &apigateway.CreateRestApiInput{
		Name:        aws.String(d.Get("name").(string)),
		Description: description,
	}

	if v, ok := d.GetOk("endpoint_configuration"); ok {
		params.EndpointConfiguration = expandApiGatewayEndpointConfiguration(v.([]interface{}))
	}

	if v, ok := d.GetOk("policy"); ok && v.(string) != "" {
		params.Policy = aws.String(v.(string))
	}

	binaryMediaTypes, binaryMediaTypesOk := d.GetOk("binary_media_types")
	if binaryMediaTypesOk {
		params.BinaryMediaTypes = expandStringList(binaryMediaTypes.([]interface{}))
	}

	minimumCompressionSize := d.Get("minimum_compression_size").(int)
	if minimumCompressionSize > -1 {
		params.MinimumCompressionSize = aws.Int64(int64(minimumCompressionSize))
	}

	gateway, err := conn.CreateRestApi(params)
	if err != nil {
		return fmt.Errorf("Error creating API Gateway: %s", err)
	}

	d.SetId(*gateway.Id)

	if body, ok := d.GetOk("body"); ok {
		log.Printf("[DEBUG] Initializing API Gateway from OpenAPI spec %s", d.Id())
		_, err := conn.PutRestApi(&apigateway.PutRestApiInput{
			RestApiId: gateway.Id,
			Mode:      aws.String(apigateway.PutModeOverwrite),
			Body:      []byte(body.(string)),
		})
		if err != nil {
			return errwrap.Wrapf("Error creating API Gateway specification: {{err}}", err)
		}
	}

	if err = resourceAwsApiGatewayRestApiRefreshResources(d, meta); err != nil {
		return err
	}

	return resourceAwsApiGatewayRestApiRead(d, meta)
}

func resourceAwsApiGatewayRestApiRefreshResources(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).apigateway

	resp, err := conn.GetResources(&apigateway.GetResourcesInput{
		RestApiId: aws.String(d.Id()),
	})
	if err != nil {
		return err
	}

	for _, item := range resp.Items {
		if *item.Path == "/" {
			d.Set("root_resource_id", item.Id)
			break
		}
	}

	return nil
}

func resourceAwsApiGatewayRestApiRead(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).apigateway
	log.Printf("[DEBUG] Reading API Gateway %s", d.Id())

	api, err := conn.GetRestApi(&apigateway.GetRestApiInput{
		RestApiId: aws.String(d.Id()),
	})
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok && awsErr.Code() == "NotFoundException" {
			log.Printf("[WARN] API Gateway (%s) not found, removing from state", d.Id())
			d.SetId("")
			return nil
		}
		return err
	}

	d.Set("name", api.Name)
	d.Set("description", api.Description)

	// The API returns policy as an escaped JSON string
	// {\\\"Version\\\":\\\"2012-10-17\\\",...}
	policy, err := strconv.Unquote(`"` + aws.StringValue(api.Policy) + `"`)
	if err != nil {
		return fmt.Errorf("error unescaping policy: %s", err)
	}
	d.Set("policy", policy)

	d.Set("binary_media_types", api.BinaryMediaTypes)

	arn := arn.ARN{
		Partition: meta.(*AWSClient).partition,
		Service:   "execute-api",
		Region:    meta.(*AWSClient).region,
		AccountID: meta.(*AWSClient).accountid,
		Resource:  d.Id(),
	}.String()
	d.Set("execution_arn", arn)

	if api.MinimumCompressionSize == nil {
		d.Set("minimum_compression_size", -1)
	} else {
		d.Set("minimum_compression_size", api.MinimumCompressionSize)
	}
	if err := d.Set("created_date", api.CreatedDate.Format(time.RFC3339)); err != nil {
		log.Printf("[DEBUG] Error setting created_date: %s", err)
	}

	if err := d.Set("endpoint_configuration", flattenApiGatewayEndpointConfiguration(api.EndpointConfiguration)); err != nil {
		return fmt.Errorf("error setting endpoint_configuration: %s", err)
	}

	return nil
}

func resourceAwsApiGatewayRestApiUpdateOperations(d *schema.ResourceData) []*apigateway.PatchOperation {
	operations := make([]*apigateway.PatchOperation, 0)

	if d.HasChange("name") {
		operations = append(operations, &apigateway.PatchOperation{
			Op:    aws.String("replace"),
			Path:  aws.String("/name"),
			Value: aws.String(d.Get("name").(string)),
		})
	}

	if d.HasChange("description") {
		operations = append(operations, &apigateway.PatchOperation{
			Op:    aws.String("replace"),
			Path:  aws.String("/description"),
			Value: aws.String(d.Get("description").(string)),
		})
	}

	if d.HasChange("policy") {
		operations = append(operations, &apigateway.PatchOperation{
			Op:    aws.String("replace"),
			Path:  aws.String("/policy"),
			Value: aws.String(d.Get("policy").(string)),
		})
	}

	if d.HasChange("minimum_compression_size") {
		minimumCompressionSize := d.Get("minimum_compression_size").(int)
		var value string
		if minimumCompressionSize > -1 {
			value = strconv.Itoa(minimumCompressionSize)
		}
		operations = append(operations, &apigateway.PatchOperation{
			Op:    aws.String("replace"),
			Path:  aws.String("/minimumCompressionSize"),
			Value: aws.String(value),
		})
	}

	if d.HasChange("binary_media_types") {
		o, n := d.GetChange("binary_media_types")
		prefix := "binaryMediaTypes"

		old := o.([]interface{})
		new := n.([]interface{})

		// Remove every binary media types. Simpler to remove and add new ones,
		// since there are no replacings.
		for _, v := range old {
			operations = append(operations, &apigateway.PatchOperation{
				Op:   aws.String("remove"),
				Path: aws.String(fmt.Sprintf("/%s/%s", prefix, escapeJsonPointer(v.(string)))),
			})
		}

		// Handle additions
		if len(new) > 0 {
			for _, v := range new {
				operations = append(operations, &apigateway.PatchOperation{
					Op:   aws.String("add"),
					Path: aws.String(fmt.Sprintf("/%s/%s", prefix, escapeJsonPointer(v.(string)))),
				})
			}
		}
	}

	if d.HasChange("endpoint_configuration.0.types") {
		// The REST API must have an endpoint type.
		// If attempting to remove the configuration, do nothing.
		if v, ok := d.GetOk("endpoint_configuration"); ok && len(v.([]interface{})) > 0 {
			m := v.([]interface{})[0].(map[string]interface{})

			operations = append(operations, &apigateway.PatchOperation{
				Op:    aws.String("replace"),
				Path:  aws.String("/endpointConfiguration/types/0"),
				Value: aws.String(m["types"].([]interface{})[0].(string)),
			})
		}
	}

	return operations
}

func resourceAwsApiGatewayRestApiUpdate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).apigateway
	log.Printf("[DEBUG] Updating API Gateway %s", d.Id())

	if d.HasChange("body") {
		if body, ok := d.GetOk("body"); ok {
			log.Printf("[DEBUG] Updating API Gateway from OpenAPI spec: %s", d.Id())
			_, err := conn.PutRestApi(&apigateway.PutRestApiInput{
				RestApiId: aws.String(d.Id()),
				Mode:      aws.String(apigateway.PutModeOverwrite),
				Body:      []byte(body.(string)),
			})
			if err != nil {
				return errwrap.Wrapf("Error updating API Gateway specification: {{err}}", err)
			}
		}
	}

	_, err := conn.UpdateRestApi(&apigateway.UpdateRestApiInput{
		RestApiId:       aws.String(d.Id()),
		PatchOperations: resourceAwsApiGatewayRestApiUpdateOperations(d),
	})

	if err != nil {
		return err
	}
	log.Printf("[DEBUG] Updated API Gateway %s", d.Id())

	return resourceAwsApiGatewayRestApiRead(d, meta)
}

func resourceAwsApiGatewayRestApiDelete(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*AWSClient).apigateway
	log.Printf("[DEBUG] Deleting API Gateway: %s", d.Id())

	return resource.Retry(10*time.Minute, func() *resource.RetryError {
		_, err := conn.DeleteRestApi(&apigateway.DeleteRestApiInput{
			RestApiId: aws.String(d.Id()),
		})
		if err == nil {
			return nil
		}

		if apigatewayErr, ok := err.(awserr.Error); ok && apigatewayErr.Code() == "NotFoundException" {
			return nil
		}

		return resource.NonRetryableError(err)
	})
}

func expandApiGatewayEndpointConfiguration(l []interface{}) *apigateway.EndpointConfiguration {
	if len(l) == 0 {
		return nil
	}

	m := l[0].(map[string]interface{})

	endpointConfiguration := &apigateway.EndpointConfiguration{
		Types: expandStringList(m["types"].([]interface{})),
	}

	return endpointConfiguration
}

func flattenApiGatewayEndpointConfiguration(endpointConfiguration *apigateway.EndpointConfiguration) []interface{} {
	if endpointConfiguration == nil {
		return []interface{}{}
	}

	m := map[string]interface{}{
		"types": flattenStringList(endpointConfiguration.Types),
	}

	return []interface{}{m}
}
