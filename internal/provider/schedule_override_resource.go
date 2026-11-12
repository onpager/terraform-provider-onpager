package provider

import "github.com/hashicorp/terraform-plugin-framework/resource"

func NewScheduleOverrideResource() resource.Resource { return &scheduleDeclarationResource{} }
