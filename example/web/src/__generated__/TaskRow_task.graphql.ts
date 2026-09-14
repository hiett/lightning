/**
 * @generated SignedSource<<b4dc980b220f24d0c8463f98fa0ba388>>
 * @lightSyntaxTransform
 * @nogrep
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ReaderFragment } from 'relay-runtime';
import { FragmentRefs } from "relay-runtime";
export type TaskRow_task$data = {
  readonly done: boolean;
  readonly id: string;
  readonly owner: {
    readonly __typename: string;
    readonly displayName: string;
    readonly email?: string;
    readonly members?: number;
  } | null | undefined;
  readonly title: string;
  readonly " $fragmentType": "TaskRow_task";
};
export type TaskRow_task$key = {
  readonly " $data"?: TaskRow_task$data;
  readonly " $fragmentSpreads": FragmentRefs<"TaskRow_task">;
};

import TaskRowRefetchQuery_graphql from './TaskRowRefetchQuery.graphql';

const node: ReaderFragment = {
  "argumentDefinitions": [],
  "kind": "Fragment",
  "metadata": {
    "refetch": {
      "connection": null,
      "fragmentPathInResult": [
        "node"
      ],
      "operation": TaskRowRefetchQuery_graphql,
      "identifierInfo": {
        "identifierField": "id",
        "identifierQueryVariableName": "id"
      }
    }
  },
  "name": "TaskRow_task",
  "selections": [
    {
      "alias": null,
      "args": null,
      "kind": "ScalarField",
      "name": "id",
      "storageKey": null
    },
    {
      "alias": null,
      "args": null,
      "kind": "ScalarField",
      "name": "title",
      "storageKey": null
    },
    {
      "alias": null,
      "args": null,
      "kind": "ScalarField",
      "name": "done",
      "storageKey": null
    },
    {
      "alias": null,
      "args": null,
      "concreteType": null,
      "kind": "LinkedField",
      "name": "owner",
      "plural": false,
      "selections": [
        {
          "alias": null,
          "args": null,
          "kind": "ScalarField",
          "name": "__typename",
          "storageKey": null
        },
        {
          "alias": null,
          "args": null,
          "kind": "ScalarField",
          "name": "displayName",
          "storageKey": null
        },
        {
          "kind": "InlineFragment",
          "selections": [
            {
              "alias": null,
              "args": null,
              "kind": "ScalarField",
              "name": "email",
              "storageKey": null
            }
          ],
          "type": "User",
          "abstractKey": null
        },
        {
          "kind": "InlineFragment",
          "selections": [
            {
              "alias": null,
              "args": null,
              "kind": "ScalarField",
              "name": "members",
              "storageKey": null
            }
          ],
          "type": "Team",
          "abstractKey": null
        }
      ],
      "storageKey": null
    }
  ],
  "type": "Task",
  "abstractKey": null
};

(node as any).hash = "46391b84391c6821b2c7c13edb26c606";

export default node;
