/**
 * @generated SignedSource<<dd8b0f140e8a6db9c4e10c309d919740>>
 * @lightSyntaxTransform
 * @nogrep
 */

/* tslint:disable */
/* eslint-disable */
// @ts-nocheck

import { ConcreteRequest } from 'relay-runtime';
export type TaskRowSetDoneMutation$variables = {
  done: boolean;
  id: string;
};
export type TaskRowSetDoneMutation$data = {
  readonly setTaskDone: {
    readonly done: boolean;
    readonly id: string;
  } | null | undefined;
};
export type TaskRowSetDoneMutation = {
  response: TaskRowSetDoneMutation$data;
  variables: TaskRowSetDoneMutation$variables;
};

const node: ConcreteRequest = (function(){
var v0 = {
  "defaultValue": null,
  "kind": "LocalArgument",
  "name": "done"
},
v1 = {
  "defaultValue": null,
  "kind": "LocalArgument",
  "name": "id"
},
v2 = [
  {
    "alias": null,
    "args": [
      {
        "kind": "Variable",
        "name": "done",
        "variableName": "done"
      },
      {
        "kind": "Variable",
        "name": "id",
        "variableName": "id"
      }
    ],
    "concreteType": "Task",
    "kind": "LinkedField",
    "name": "setTaskDone",
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
        "name": "done",
        "storageKey": null
      }
    ],
    "storageKey": null
  }
];
return {
  "fragment": {
    "argumentDefinitions": [
      (v0/*: any*/),
      (v1/*: any*/)
    ],
    "kind": "Fragment",
    "metadata": null,
    "name": "TaskRowSetDoneMutation",
    "selections": (v2/*: any*/),
    "type": "Mutation",
    "abstractKey": null
  },
  "kind": "Request",
  "operation": {
    "argumentDefinitions": [
      (v1/*: any*/),
      (v0/*: any*/)
    ],
    "kind": "Operation",
    "name": "TaskRowSetDoneMutation",
    "selections": (v2/*: any*/)
  },
  "params": {
    "cacheID": "3c9acd9d1ae48e5fb1ff55f9c5567da8",
    "id": null,
    "metadata": {},
    "name": "TaskRowSetDoneMutation",
    "operationKind": "mutation",
    "text": "mutation TaskRowSetDoneMutation(\n  $id: ID!\n  $done: Boolean!\n) {\n  setTaskDone(id: $id, done: $done) {\n    id\n    done\n  }\n}\n"
  }
};
})();

(node as any).hash = "7070b81987751ba82942f28d4d03ab36";

export default node;
