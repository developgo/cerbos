// Copyright 2021-2022 Zenauth Ltd.
// SPDX-License-Identifier: Apache-2.0

//go:build tests
// +build tests

package internal

import (
	"context"
	"io/ioutil"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/wrapperspb"

	schemav1 "github.com/cerbos/cerbos/api/genpb/cerbos/schema/v1"
	"github.com/cerbos/cerbos/internal/namer"
	"github.com/cerbos/cerbos/internal/policy"
	"github.com/cerbos/cerbos/internal/storage"
	"github.com/cerbos/cerbos/internal/test"
	"github.com/cerbos/cerbos/internal/util"
)

const timeout = 1 * time.Second

//nolint:gomnd
func TestSuite(store DBStorage) func(*testing.T) {
	//nolint:thelper
	return func(t *testing.T) {
		ctx, cancelFunc := context.WithCancel(context.Background())
		defer cancelFunc()
		rp := policy.Wrap(test.GenResourcePolicy(test.NoMod()))
		pp := policy.Wrap(test.GenPrincipalPolicy(test.NoMod()))
		dr := policy.Wrap(test.GenDerivedRoles(test.NoMod()))
		rpx := policy.Wrap(test.GenResourcePolicy(test.PrefixAndSuffix("x", "x")))
		drx := policy.Wrap(test.GenDerivedRoles(test.PrefixAndSuffix("x", "x")))
		sch := test.ReadSchemaFromFile(t, test.PathToDir(t, "store/_schemas/leave_request.json"))
		const schID = "leave_request"

		t.Run("add", func(t *testing.T) {
			checkEvents := storage.TestSubscription(store)
			require.NoError(t, store.AddOrUpdate(ctx, rp, pp, dr, rpx, drx))

			wantEvents := []storage.Event{
				{Kind: storage.EventAddOrUpdatePolicy, PolicyID: rp.ID},
				{Kind: storage.EventAddOrUpdatePolicy, PolicyID: pp.ID},
				{Kind: storage.EventAddOrUpdatePolicy, PolicyID: dr.ID},
				{Kind: storage.EventAddOrUpdatePolicy, PolicyID: rpx.ID},
				{Kind: storage.EventAddOrUpdatePolicy, PolicyID: drx.ID},
			}
			checkEvents(t, timeout, wantEvents...)
		})

		t.Run("get_compilation_unit_with_deps", func(t *testing.T) {
			have, err := store.GetCompilationUnits(ctx, rp.ID)
			require.NoError(t, err)
			require.Len(t, have, 1)
			require.Contains(t, have, rp.ID)

			haveRec := have[rp.ID]
			require.Equal(t, rp.ID, haveRec.ModID)
			require.Len(t, haveRec.Definitions, 2)

			require.Contains(t, haveRec.Definitions, rp.ID)
			require.Empty(t, cmp.Diff(rp, haveRec.Definitions[rp.ID], protocmp.Transform()))

			require.Contains(t, haveRec.Definitions, dr.ID)
			require.Empty(t, cmp.Diff(dr, haveRec.Definitions[dr.ID], protocmp.Transform()))
		})

		t.Run("get_compilation_unit_without_deps", func(t *testing.T) {
			have, err := store.GetCompilationUnits(ctx, pp.ID)
			require.NoError(t, err)
			require.Len(t, have, 1)
			require.Contains(t, have, pp.ID)

			haveRec := have[pp.ID]
			require.Equal(t, pp.ID, haveRec.ModID)
			require.Len(t, haveRec.Definitions, 1)

			require.Contains(t, haveRec.Definitions, pp.ID)
			require.Empty(t, cmp.Diff(pp, haveRec.Definitions[pp.ID], protocmp.Transform()))
		})

		t.Run("get_multiple_compilation_units", func(t *testing.T) {
			have, err := store.GetCompilationUnits(ctx, rp.ID, pp.ID)
			require.NoError(t, err)
			require.Len(t, have, 2)
			require.Contains(t, have, rp.ID)
			require.Contains(t, have, pp.ID)

			haveRP := have[rp.ID]
			require.Equal(t, rp.ID, haveRP.ModID)
			require.Len(t, haveRP.Definitions, 2)

			require.Contains(t, haveRP.Definitions, rp.ID)
			require.Empty(t, cmp.Diff(rp, haveRP.Definitions[rp.ID], protocmp.Transform()))

			require.Contains(t, haveRP.Definitions, dr.ID)
			require.Empty(t, cmp.Diff(dr, haveRP.Definitions[dr.ID], protocmp.Transform()))

			havePP := have[pp.ID]
			require.Equal(t, pp.ID, havePP.ModID)
			require.Len(t, havePP.Definitions, 1)

			require.Contains(t, havePP.Definitions, pp.ID)
			require.Empty(t, cmp.Diff(pp, havePP.Definitions[pp.ID], protocmp.Transform()))
		})

		t.Run("get_non_existent_compilation_unit", func(t *testing.T) {
			p := policy.Wrap(test.GenResourcePolicy(test.PrefixAndSuffix("y", "y")))
			have, err := store.GetCompilationUnits(ctx, p.ID)
			require.NoError(t, err)
			require.Empty(t, have)
		})

		t.Run("get_dependents", func(t *testing.T) {
			have, err := store.GetDependents(ctx, dr.ID)
			require.NoError(t, err)

			require.Len(t, have, 1)
			require.Contains(t, have, dr.ID)

			require.Len(t, have[dr.ID], 1)
			require.Contains(t, have[dr.ID], rp.ID)
		})

		t.Run("get_policy", func(t *testing.T) {
			t.Run("should be able to get policy", func(t *testing.T) {
				t.Log(namer.PolicyKeyFromFQN(dr.FQN))
				p, err := store.LoadPolicy(ctx, namer.PolicyKeyFromFQN(dr.FQN))
				require.NoError(t, err)
				require.NotEmpty(t, p)
			})

			t.Run("should have correct metadata", func(t *testing.T) {
				t.Log(namer.PolicyKeyFromFQN(dr.FQN))
				policies, err := store.LoadPolicy(ctx, namer.PolicyKeyFromFQN(dr.FQN))
				require.NoError(t, err)
				require.NotEmpty(t, policies)
				require.NotEmpty(t, policies[0].Metadata)
				require.Equal(t, policies[0].Metadata.StoreIdentifer, namer.PolicyKeyFromFQN(dr.FQN))
				require.Equal(t, policies[0].Metadata.Hash, wrapperspb.UInt64(util.HashPB(policies[0], policy.IgnoreHashFields)))
			})
		})

		t.Run("list_policies", func(t *testing.T) {
			t.Run("should be able to list policies", func(t *testing.T) {
				policies, err := store.ListPolicyIDs(ctx)
				require.NoError(t, err)
				require.NotEmpty(t, policies)
			})
		})

		t.Run("delete", func(t *testing.T) {
			checkEvents := storage.TestSubscription(store)

			err := store.Delete(ctx, rpx.ID)
			require.NoError(t, err)

			have, err := store.GetCompilationUnits(ctx, rpx.ID)
			require.NoError(t, err)
			require.Empty(t, have)

			checkEvents(t, timeout, storage.Event{Kind: storage.EventDeletePolicy, PolicyID: rpx.ID})
		})

		t.Run("add_schema", func(t *testing.T) {
			checkEvents := storage.TestSubscription(store)
			require.NoError(t, store.AddOrUpdateSchema(ctx, &schemav1.Schema{Id: schID, Definition: sch}))

			checkEvents(t, timeout, storage.NewSchemaEvent(storage.EventAddOrUpdateSchema, schID))
		})

		t.Run("get_schema", func(t *testing.T) {
			t.Run("should be able to get schema", func(t *testing.T) {
				schema, err := store.LoadSchema(ctx, schID)
				require.NoError(t, err)
				require.NotEmpty(t, schema)
				schBytes, err := ioutil.ReadAll(schema)
				require.NoError(t, err)
				require.NotEmpty(t, schBytes)
				require.JSONEq(t, string(sch), string(schBytes))
			})
		})

		t.Run("delete_schema", func(t *testing.T) {
			checkEvents := storage.TestSubscription(store)

			err := store.DeleteSchema(ctx, schID)
			require.NoError(t, err)

			have, err := store.LoadSchema(ctx, schID)
			require.Error(t, err)
			require.Empty(t, have)

			checkEvents(t, timeout, storage.NewSchemaEvent(storage.EventDeleteSchema, schID))
		})
	}
}
