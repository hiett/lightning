/**
 * @generated SignedSource<<15917e25bab09b1f829f5000067ec72c>>
 * @lightSyntaxTransform
 * @nogrep
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
import { FragmentRefs } from "relay-runtime";
export type TaskListAddMutation$variables = {
  connections: ReadonlyArray<string>;
  ownerId: string;
  title: string;
};
export type TaskListAddMutation$data = {
  readonly addTask: {
    readonly id: string;
    readonly " $fragmentSpreads": FragmentRefs<"TaskRow_task">;
  } | null | undefined;
};
export type TaskListAddMutation = {
  response: TaskListAddMutation$data;
  variables: TaskListAddMutation$variables;
};

const node: ConcreteRequest = (function(){
var v0 = {
  "defaultValue": null,
  "kind": "LocalArgument",
  "name": "connections"
},
v1 = {
  "defaultValue": null,
  "kind": "LocalArgument",
  "name": "ownerId"
},
v2 = {
  "defaultValue": null,
  "kind": "LocalArgument",
  "name": "title"
},
v3 = [
  {
    "kind": "Variable",
    "name": "ownerId",
    "variableName": "ownerId"
  },
  {
    "kind": "Variable",
    "name": "title",
    "variableName": "title"
  }
],
v4 = {
  "alias": null,
  "args": null,
  "kind": "ScalarField",
  "name": "id",
  "storageKey": null
};
return {
  "fragment": {
    "argumentDefinitions": [
      (v0/*: any*/),
      (v1/*: any*/),
      (v2/*: any*/)
    ],
    "kind": "Fragment",
    "metadata": null,
    "name": "TaskListAddMutation",
    "selections": [
      {
        "alias": null,
        "args": (v3/*: any*/),
        "concreteType": "Task",
        "kind": "LinkedField",
        "name": "addTask",
        "plural": false,
        "selections": [
          (v4/*: any*/),
          {
            "args": null,
            "kind": "FragmentSpread",
            "name": "TaskRow_task"
          }
        ],
        "storageKey": null
      }
    ],
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": [
      (v2/*: any*/),
      (v1/*: any*/),
      (v0/*: any*/)
    ],
    "kind": "Operation",
    "name": "TaskListAddMutation",
    "selections": [
      {
        "alias": null,
        "args": (v3/*: any*/),
        "concreteType": "Task",
        "kind": "LinkedField",
        "name": "addTask",
        "plural": false,
        "selections": [
          (v4/*: any*/),
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
              },
              (v4/*: any*/)
            ],
            "storageKey": null
          }
        ],
        "storageKey": null
      },
      {
        "alias": null,
        "args": (v3/*: any*/),
        "filters": null,
        "handle": "appendNode",
        "key": "",
        "kind": "LinkedHandle",
        "name": "addTask",
        "handleArgs": [
          {
            "kind": "Variable",
            "name": "connections",
            "variableName": "connections"
          },
          {
            "kind": "Literal",
            "name": "edgeTypeName",
            "value": "TaskEdge"
          }
        ]
      }
    ]
  },
  "params": {
    "cacheID": "e2e5bb3862fa0cb66c54b2d7ef89564b",
    "id": null,
    "metadata": {},
    "name": "TaskListAddMutation",
    "operationKind": "mutation",
    "text": "mutation TaskListAddMutation(\n  $title: String!\n  $ownerId: ID!\n) {\n  addTask(title: $title, ownerId: $ownerId) {\n    id\n    ...TaskRow_task\n  }\n}\n\nfragment TaskRow_task on Task {\n  id\n  title\n  done\n  owner {\n    __typename\n    displayName\n    ... on User {\n      email\n    }\n    ... on Team {\n      members\n    }\n    id\n  }\n}\n"
  }
};
})();

(node as any).hash = "23d5a607149135fc8e41d4923129e5ed";

export default node;
