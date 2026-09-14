/**
 * @generated SignedSource<<379a6a5aa78111ed2f5a89aa0adac896>>
 * @lightSyntaxTransform
 * @nogrep
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type LiveTasksSubscription$variables = Record<PropertyKey, never>;
export type LiveTasksSubscription$data = {
  readonly tasks: {
    readonly edges: ReadonlyArray<{
      readonly node: {
        readonly done: boolean;
        readonly id: string;
        readonly title: string;
      };
    }>;
    readonly totalCount: any;
  };
};
export type LiveTasksSubscription = {
  response: LiveTasksSubscription$data;
  variables: LiveTasksSubscription$variables;
};

const node: ConcreteRequest = (function(){
var v0 = [
  {
    "alias": null,
    "args": [
      {
        "kind": "Literal",
        "name": "first",
        "value": 100
      }
    ],
    "concreteType": "TaskConnection",
    "kind": "LinkedField",
    "name": "tasks",
    "plural": false,
    "selections": [
      {
        "alias": null,
        "args": null,
        "kind": "ScalarField",
        "name": "totalCount",
        "storageKey": null
      },
      {
        "alias": null,
        "args": null,
        "concreteType": "TaskEdge",
        "kind": "LinkedField",
        "name": "edges",
        "plural": true,
        "selections": [
          {
            "alias": null,
            "args": null,
            "concreteType": "Task",
            "kind": "LinkedField",
            "name": "node",
            "plural": false,
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
              }
            ],
            "storageKey": null
          }
        ],
        "storageKey": null
      }
    ],
    "storageKey": "tasks(first:100)"
  }
];
return {
  "fragment": {
    "argumentDefinitions": [],
    "kind": "Fragment",
    "metadata": null,
    "name": "LiveTasksSubscription",
    "selections": (v0/*: any*/),
    "type": "Subscription",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": [],
    "kind": "Operation",
    "name": "LiveTasksSubscription",
    "selections": (v0/*: any*/)
  },
  "params": {
    "cacheID": "5ae19c229a5b19498e6325bd157a635d",
    "id": null,
    "metadata": {},
    "name": "LiveTasksSubscription",
    "operationKind": "subscription",
    "text": "subscription LiveTasksSubscription {\n  tasks(first: 100) {\n    totalCount\n    edges {\n      node {\n        id\n        title\n        done\n      }\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "08112c635446b65c55c3a77a397f14d5";

export default node;
