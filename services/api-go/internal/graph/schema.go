package graph

import (
	"github.com/graphql-go/graphql"
)

func NewSchema(r *Resolver) (graphql.Schema, error) {
	findingType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Finding",
		Fields: graphql.Fields{
			"id":          &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},
			"checkName":   &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"result":      &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"evidence":    &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"ruleVersion": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"createdAt":   &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		},
	})

	jobType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Job",
		Fields: graphql.Fields{
			"id":           &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},
			"jobType":      &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"status":       &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"attemptCount": &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
			"lastError":    &graphql.Field{Type: graphql.String},
			"result":       &graphql.Field{Type: graphql.String},
			"createdAt":    &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"updatedAt":    &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		},
	})

	assetType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Asset",
		Fields: graphql.Fields{
			"id":          &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},
			"kind":        &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"contentType": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"widthPx":     &graphql.Field{Type: graphql.Int},
			"heightPx":    &graphql.Field{Type: graphql.Int},
			"sha256":      &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			// storageKey: an internal storage-backend key, not a secret on
			// its own (no auth gap beyond what already exists - point 26) -
			// exposed so the eval harness can independently re-read stored
			// bytes and verify them, rather than trusting DB metadata alone.
			"storageKey": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"createdAt":  &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		},
	})

	repairType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Repair",
		Fields: graphql.Fields{
			"id":             &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},
			"idempotencyKey": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"status":         &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"reason":         &graphql.Field{Type: graphql.String},
			"diagnosis":      &graphql.Field{Type: graphql.String},
			"createdAt":      &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		},
	})

	clarificationType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Clarification",
		Fields: graphql.Fields{
			"id":            &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},
			"question":      &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"answer":        &graphql.Field{Type: graphql.String},
			"answeredAt":    &graphql.Field{Type: graphql.String},
			"invalidatedAt": &graphql.Field{Type: graphql.String},
			"createdAt":     &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		},
	})

	orderType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Order",
		Fields: graphql.Fields{
			"id":                &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},
			"ownerId":           &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"productType":       &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"declaredWidth":     &graphql.Field{Type: graphql.NewNonNull(graphql.Float)},
			"declaredHeight":    &graphql.Field{Type: graphql.NewNonNull(graphql.Float)},
			"declaredUnit":      &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"customerRequest":   &graphql.Field{Type: graphql.String},
			"artworkVersion":    &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
			"intent":            &graphql.Field{Type: graphql.String},
			"artworkIsTrimOnly": &graphql.Field{Type: graphql.Boolean},
			"caseVersion":       &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
			"artworkStatus":     &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"proofStatus":       &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"productionStatus":  &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"createdAt":         &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"updatedAt":         &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"findings": &graphql.Field{
				Type:    graphql.NewList(findingType),
				Resolve: r.resolveOrderFindings,
			},
			"jobs": &graphql.Field{
				Type:    graphql.NewList(jobType),
				Resolve: r.resolveOrderJobs,
			},
			"assets": &graphql.Field{
				Type:    graphql.NewList(assetType),
				Resolve: r.resolveOrderAssets,
			},
			"clarifications": &graphql.Field{
				Type:    graphql.NewList(clarificationType),
				Resolve: r.resolveOrderClarifications,
			},
			"repairs": &graphql.Field{
				Type:    graphql.NewList(repairType),
				Resolve: r.resolveOrderRepairs,
			},
		},
	})

	uploadTicketType := graphql.NewObject(graphql.ObjectConfig{
		Name: "UploadTicket",
		Fields: graphql.Fields{
			"uploadUrl": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"expiresAt": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		},
	})

	createOrderInput := graphql.NewInputObject(graphql.InputObjectConfig{
		Name: "CreateOrderInput",
		Fields: graphql.InputObjectConfigFieldMap{
			"ownerId":         &graphql.InputObjectFieldConfig{Type: graphql.NewNonNull(graphql.String)},
			"productType":     &graphql.InputObjectFieldConfig{Type: graphql.NewNonNull(graphql.String)},
			"declaredWidth":   &graphql.InputObjectFieldConfig{Type: graphql.NewNonNull(graphql.Float)},
			"declaredHeight":  &graphql.InputObjectFieldConfig{Type: graphql.NewNonNull(graphql.Float)},
			"declaredUnit":    &graphql.InputObjectFieldConfig{Type: graphql.NewNonNull(graphql.String)},
			"customerRequest": &graphql.InputObjectFieldConfig{Type: graphql.String},
			"intent":          &graphql.InputObjectFieldConfig{Type: graphql.String},
		},
	})

	queryType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Query",
		Fields: graphql.Fields{
			"order": &graphql.Field{
				Type: orderType,
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)},
				},
				Resolve: r.resolveOrder,
			},
			"job": &graphql.Field{
				Type: jobType,
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)},
				},
				Resolve: r.resolveJob,
			},
		},
	})

	mutationType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Mutation",
		Fields: graphql.Fields{
			"createOrder": &graphql.Field{
				Type: graphql.NewNonNull(orderType),
				Args: graphql.FieldConfigArgument{
					"input": &graphql.ArgumentConfig{Type: graphql.NewNonNull(createOrderInput)},
				},
				Resolve: r.resolveCreateOrder,
			},
			"createUpload": &graphql.Field{
				Type: graphql.NewNonNull(uploadTicketType),
				Args: graphql.FieldConfigArgument{
					"orderId":     &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)},
					"contentType": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
				},
				Resolve: r.resolveCreateUpload,
			},
			"startResolution": &graphql.Field{
				Type: graphql.NewNonNull(jobType),
				Args: graphql.FieldConfigArgument{
					"orderId": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)},
				},
				Resolve: r.resolveStartResolution,
			},
			"confirmTrim": &graphql.Field{
				Type: graphql.NewNonNull(orderType),
				Args: graphql.FieldConfigArgument{
					"orderId":           &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)},
					"artworkIsTrimOnly": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.Boolean)},
					"caseVersion":       &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.Int)},
				},
				Resolve: r.resolveConfirmTrim,
			},
			// answerClarification and requestRepair are declared now so the
			// schema is frozen from Day 1, per the brief - they are wired up
			// in Day 4 and Day 3 respectively.
			"answerClarification": &graphql.Field{
				Type: graphql.NewNonNull(orderType),
				Args: graphql.FieldConfigArgument{
					"clarificationId": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)},
					"answer":          &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
					"caseVersion":     &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.Int)},
				},
				Resolve: r.resolveAnswerClarification,
			},
			"requestRepair": &graphql.Field{
				Type: graphql.NewNonNull(jobType),
				Args: graphql.FieldConfigArgument{
					"orderId":        &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)},
					"idempotencyKey": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
					"caseVersion":    &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.Int)},
				},
				Resolve: r.resolveRequestRepair,
			},
		},
	})

	return graphql.NewSchema(graphql.SchemaConfig{
		Query:    queryType,
		Mutation: mutationType,
	})
}
