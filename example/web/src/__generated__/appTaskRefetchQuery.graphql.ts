/**
 * @generated SignedSource<<036a684d3960fc22d362c9db09be0ce0>>
 * @lightSyntaxTransform
 * @nogrep
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type appTaskRefetchQuery$variables = {
  id: string;
};
export type appTaskRefetchQuery$data = {
  readonly node: {
    readonly __typename: string;
    readonly done?: boolean;
    readonly id: string;
    readonly title?: string;
  } | null | undefined;
};
export type appTaskRefetchQuery = {
  response: appTaskRefetchQuery$data;
  variables: appTaskRefetchQuery$variables;
};

const node: ConcreteRequest = (function(){
var v0 = [
  {
    "defaultValue": null,
    "kind": "LocalArgument",
    "name": "id"
  }
],
v1 = [
  {
    "alias": null,
    "args": [
      {
        "kind": "Variable",
        "name": "id",
        "variableName": "id"
      }
    ],
    "concreteType": null,
    "kind": "LinkedField",
    "name": "node",
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
        "name": "id",
        "storageKey": null
      },
      {
        "kind": "InlineFragment",
        "selections": [
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
        "type": "Task",
        "abstractKey": null
      }
    ],
    "storageKey": null
  }
];
return {
  "fragment": {
    "argumentDefinitions": (v0/*: any*/),
    "kind": "Fragment",
    "metadata": null,
    "name": "appTaskRefetchQuery",
    "selections": (v1/*: any*/),
    "type": "Query",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": (v0/*: any*/),
    "kind": "Operation",
    "name": "appTaskRefetchQuery",
    "selections": (v1/*: any*/)
  },
  "params": {
    "cacheID": "73d4f9d13713ea02e793f22f7aa3993e",
    "id": null,
    "metadata": {},
    "name": "appTaskRefetchQuery",
    "operationKind": "query",
    "text": "query appTaskRefetchQuery(\n  $id: ID!\n) {\n  node(id: $id) {\n    __typename\n    id\n    ... on Task {\n      title\n      done\n    }\n  }\n}\n"
  }
};
})();

(node as any).hash = "0ecf4c9de2bf362f251978fe8ff4d1b6";

export default node;
