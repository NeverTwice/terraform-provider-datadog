package datadog

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/zorkian/go-datadog-api"
)

func resourceDatadogServiceLevelObjective() *schema.Resource {
	return &schema.Resource{
		Create: resourceDatadogServiceLevelObjectiveCreate,
		Read:   resourceDatadogServiceLevelObjectiveRead,
		Update: resourceDatadogServiceLevelObjectiveUpdate,
		Delete: resourceDatadogServiceLevelObjectiveDelete,
		Exists: resourceDatadogServiceLevelObjectiveExists,
		Importer: &schema.ResourceImporter{
			State: resourceDatadogServiceLevelObjectiveImport,
		},

		Schema: map[string]*schema.Schema{
			// Common
			"name": {
				Type:     schema.TypeString,
				Required: true,
			},
			"description": {
				Type:     schema.TypeString,
				Optional: true,
				StateFunc: func(val interface{}) string {
					return strings.TrimSpace(val.(string))
				},
			},
			"tags": {
				// we use TypeSet to represent tags, paradoxically to be able to maintain them ordered;
				// we order them explicitly in the read/create/update methods of this resource and using
				// TypeSet makes Terraform ignore differences in order when creating a plan
				Type:     schema.TypeSet,
				Optional: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},
			"thresholds": {
				Type:     schema.TypeList,
				Required: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"timeframe": {
							Type:     schema.TypeString,
							Required: true,
						},
						"target": {
							Type:             schema.TypeFloat,
							Required:         true,
							DiffSuppressFunc: suppressDataDogFloatIntDiff,
						},
						"target_display": {
							Type:             schema.TypeString,
							Optional:         true,
							DiffSuppressFunc: suppressDataDogSLODisplayValueDiff,
						},
						"warning": {
							Type:             schema.TypeFloat,
							Optional:         true,
							DiffSuppressFunc: suppressDataDogFloatIntDiff,
						},
						"warning_display": {
							Type:             schema.TypeString,
							Optional:         true,
							DiffSuppressFunc: suppressDataDogSLODisplayValueDiff,
						},
					},
				},
			},
			"type": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: ValidateServiceLevelObjectiveTypeString,
			},

			// Metric-Based SLO
			"query": {
				// we use TypeList here because of https://github.com/hashicorp/terraform/issues/6215/
				Type:          schema.TypeList,
				MaxItems:      1,
				Optional:      true,
				ConflictsWith: []string{"monitor_ids", "groups"},
				Description:   "The metric query of good / total events",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"numerator": {
							Type:     schema.TypeString,
							Required: true,
							StateFunc: func(val interface{}) string {
								return strings.TrimSpace(val.(string))
							},
						},
						"denominator": {
							Type:     schema.TypeString,
							Required: true,
							StateFunc: func(val interface{}) string {
								return strings.TrimSpace(val.(string))
							},
						},
					},
				},
			},

			// Monitor-Based SLO
			"monitor_ids": {
				Type:          schema.TypeSet,
				Optional:      true,
				ConflictsWith: []string{"query"},
				Description:   "A static set of monitor IDs to use as part of the SLO",
				Elem:          &schema.Schema{Type: schema.TypeInt, MinItems: 1},
			},
			// NOTE: This feature was introduced but it never worked and then it was removed.
			// We didn't trigger a major release since it never worked. However, this may be introduced later again.
			// Keeping this here for now and we removed the related code.
			"monitor_search": {
				Type:     schema.TypeString,
				Optional: true,
				Removed:  "Feature is not yet supported",
				Computed: true,
			},
			"groups": {
				Type:          schema.TypeSet,
				Optional:      true,
				Description:   "A static set of groups to filter monitor-based SLOs",
				ConflictsWith: []string{"query"},
				Elem:          &schema.Schema{Type: schema.TypeString, MinItems: 1},
			},
		},
	}
}

// ValidateServiceLevelObjectiveTypeString is a ValidateFunc that ensures the SLO is of one of the supported types
func ValidateServiceLevelObjectiveTypeString(v interface{}, k string) (ws []string, errors []error) {
	switch v.(string) {
	case datadog.ServiceLevelObjectiveTypeMonitor:
		break
	case datadog.ServiceLevelObjectiveTypeMetric:
		break
	default:
		errors = append(errors, fmt.Errorf("invalid type %s specified for SLO", v.(string)))
	}
	return
}

func buildServiceLevelObjectiveStruct(d *schema.ResourceData) *datadog.ServiceLevelObjective {

	slo := &datadog.ServiceLevelObjective{
		ID:   datadog.String(d.Id()),
		Name: datadog.String(d.Get("name").(string)),
		Type: datadog.String(d.Get("type").(string)),
	}

	if attr, ok := d.GetOk("description"); ok {
		slo.SetDescription(attr.(string))
	}

	switch slo.GetType() {
	case datadog.ServiceLevelObjectiveTypeMonitor:
		// monitor type
		if attr, ok := d.GetOk("monitor_ids"); ok {
			s := make([]int, 0)
			for _, v := range attr.(*schema.Set).List() {
				s = append(s, v.(int))
			}
			sort.Ints(s)
			slo.MonitorIDs = s
		}
		if attr, ok := d.GetOk("groups"); ok {
			s := make([]string, 0)
			for _, v := range attr.(*schema.Set).List() {
				s = append(s, v.(string))
			}
			sort.Strings(s)
			slo.Groups = s
		}
	default:
		// metric type
		if attr, ok := d.GetOk("query"); ok {
			queries := make([]map[string]interface{}, 0)
			raw := attr.([]interface{})
			for _, rawQuery := range raw {
				if query, ok := rawQuery.(map[string]interface{}); ok {
					queries = append(queries, query)
				}
			}
			if len(queries) >= 1 {
				// only use the first defined query
				slo.SetQuery(datadog.ServiceLevelObjectiveMetricQuery{
					Numerator:   datadog.String(queries[0]["numerator"].(string)),
					Denominator: datadog.String(queries[0]["denominator"].(string)),
				})
			}
		}
	}

	if attr, ok := d.GetOk("tags"); ok {
		s := make([]string, 0)
		for _, v := range attr.(*schema.Set).List() {
			s = append(s, v.(string))
		}
		sort.Strings(s)
		slo.Tags = s
	}

	if _, ok := d.GetOk("thresholds"); ok {
		numThresholds := d.Get("thresholds.#").(int)
		sloThresholds := make(datadog.ServiceLevelObjectiveThresholds, 0)
		for i := 0; i < numThresholds; i++ {
			prefix := fmt.Sprintf("thresholds.%d.", i)
			t := datadog.ServiceLevelObjectiveThreshold{}

			if tf, ok := d.GetOk(prefix + "timeframe"); ok {
				t.TimeFrame = datadog.String(tf.(string))
			}

			if targetValue, ok := d.GetOk(prefix + "target"); ok {
				if f, ok := floatOk(targetValue); ok {
					t.Target = datadog.Float64(f)
				}
			}

			if warningValue, ok := d.GetOk(prefix + "warning"); ok {
				if f, ok := floatOk(warningValue); ok {
					t.Warning = datadog.Float64(f)
				}
			}

			if targetDisplayValue, ok := d.GetOk(prefix + "target_display"); ok {
				if s, ok := targetDisplayValue.(string); ok && strings.TrimSpace(s) != "" {
					t.TargetDisplay = datadog.String(strings.TrimSpace(targetDisplayValue.(string)))
				}
			}

			if warningDisplayValue, ok := d.GetOk(prefix + "warning_display"); ok {
				if s, ok := warningDisplayValue.(string); ok && strings.TrimSpace(s) != "" {
					t.WarningDisplay = datadog.String(strings.TrimSpace(warningDisplayValue.(string)))
				}
			}
			sloThresholds = append(sloThresholds, &t)
		}
		if len(sloThresholds) > 0 {
			sort.Sort(sloThresholds)
			slo.Thresholds = sloThresholds
		}
	}

	return slo
}

func floatOk(val interface{}) (float64, bool) {
	switch val.(type) {
	case float64:
		return val.(float64), true
	case *float64:
		return *(val.(*float64)), true
	case string:
		f, err := strconv.ParseFloat(val.(string), 64)
		if err == nil {
			return f, true
		}
	case *string:
		f, err := strconv.ParseFloat(*(val.(*string)), 64)
		if err == nil {
			return f, true
		}
	default:
		return 0, false
	}
	return 0, false
}

func resourceDatadogServiceLevelObjectiveCreate(d *schema.ResourceData, meta interface{}) error {
	providerConf := meta.(*ProviderConfiguration)
	client := providerConf.CommunityClient

	slo := buildServiceLevelObjectiveStruct(d)
	slo, err := client.CreateServiceLevelObjective(slo)
	if err != nil {
		return translateClientError(err, "error creating service level objective")
	}

	d.SetId(slo.GetID())

	return resourceDatadogServiceLevelObjectiveRead(d, meta)
}

func resourceDatadogServiceLevelObjectiveExists(d *schema.ResourceData, meta interface{}) (b bool, e error) {
	// Exists - This is called to verify a resource still exists. It is called prior to Read,
	// and lowers the burden of Read to be able to assume the resource exists.
	providerConf := meta.(*ProviderConfiguration)
	client := providerConf.CommunityClient

	if _, err := client.GetServiceLevelObjective(d.Id()); err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "not found") || strings.Contains(errStr, "no slo specified") {
			return false, nil
		}
		return false, translateClientError(err, "error checking service level objective exists")
	}

	return true, nil
}

func resourceDatadogServiceLevelObjectiveRead(d *schema.ResourceData, meta interface{}) error {
	providerConf := meta.(*ProviderConfiguration)
	client := providerConf.CommunityClient

	slo, err := client.GetServiceLevelObjective(d.Id())
	if err != nil {
		return translateClientError(err, "error getting service level objective")
	}

	thresholds := make([]map[string]interface{}, 0)
	sort.Sort(slo.Thresholds)
	for _, threshold := range slo.Thresholds {
		t := map[string]interface{}{
			"timeframe": threshold.GetTimeFrame(),
			"target":    threshold.GetTarget(),
		}
		if warning, ok := threshold.GetWarningOk(); ok {
			t["warning"] = warning
		}
		if targetDisplay, ok := threshold.GetTargetDisplayOk(); ok {
			t["target_display"] = targetDisplay
		}
		if warningDisplay, ok := threshold.GetWarningDisplayOk(); ok {
			t["warning_display"] = warningDisplay
		}
		thresholds = append(thresholds, t)
	}

	tags := make([]string, 0)
	for _, s := range slo.Tags {
		tags = append(tags, s)
	}
	sort.Strings(tags)

	d.Set("name", slo.GetName())
	d.Set("description", slo.GetDescription())
	d.Set("type", slo.GetType())
	d.Set("tags", tags)
	d.Set("thresholds", thresholds)
	switch slo.GetType() {
	case datadog.ServiceLevelObjectiveTypeMonitor:
		// monitor type
		if len(slo.MonitorIDs) > 0 {
			sort.Ints(slo.MonitorIDs)
			d.Set("monitor_ids", slo.MonitorIDs)
		}
		sort.Strings(slo.Groups)
		d.Set("groups", slo.Groups)
	default:
		// metric type
		query := make(map[string]interface{})
		q := slo.GetQuery()
		query["numerator"] = q.GetNumerator()
		query["denominator"] = q.GetDenominator()
		d.Set("query", []map[string]interface{}{query})
	}

	return nil
}

func resourceDatadogServiceLevelObjectiveUpdate(d *schema.ResourceData, meta interface{}) error {
	providerConf := meta.(*ProviderConfiguration)
	client := providerConf.CommunityClient
	slo := buildServiceLevelObjectiveStruct(d)

	if _, err := client.UpdateServiceLevelObjective(slo); err != nil {
		return translateClientError(err, "error updating service level objective")
	}

	return resourceDatadogServiceLevelObjectiveRead(d, meta)
}

func resourceDatadogServiceLevelObjectiveDelete(d *schema.ResourceData, meta interface{}) error {
	providerConf := meta.(*ProviderConfiguration)
	client := providerConf.CommunityClient

	err := client.DeleteServiceLevelObjective(d.Id())
	if err != nil {
		return translateClientError(err, "error deleting service level objective")
	}
	return nil

}

func resourceDatadogServiceLevelObjectiveImport(d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	if err := resourceDatadogServiceLevelObjectiveRead(d, meta); err != nil {
		return nil, err
	}
	return []*schema.ResourceData{d}, nil
}

// Ignore any diff that results from the mix of *_display string values from the
// DataDog API.
func suppressDataDogSLODisplayValueDiff(k, old, new string, d *schema.ResourceData) bool {
	sloType := d.Get("type")
	if sloType == datadog.ServiceLevelObjectiveTypeMonitor {
		// always suppress monitor type, this is controlled via API.
		return true
	}

	// metric type otherwise
	if old == "" || new == "" {
		// always suppress if not specified
		return true
	}

	return suppressDataDogFloatIntDiff(k, old, new, d)
}
