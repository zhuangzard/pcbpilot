#!/usr/bin/env node

import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';
import {
  buildBlocksArgs,
  buildActionCallArgs,
  buildWorkflowArgs,
  DOMAIN_NAMES,
  filterActions,
  runEasyeda,
  toMcpResult,
} from './core.mjs';

const catalogExecution = await runEasyeda(['actions'], 30_000);
if (!catalogExecution.ok || !Array.isArray(catalogExecution.result)) {
  process.stderr.write(`pcbpilot-mcp: cannot load action catalog: ${JSON.stringify(catalogExecution)}\n`);
  process.exit(1);
}
const actions = catalogExecution.result.filter((action) => DOMAIN_NAMES.includes(action.domain));
const byName = new Map(actions.map((action) => [action.name, action]));

const server = new Server(
  { name: 'pcbpilot-mcp', version: '0.18.3' },
  {
    capabilities: { tools: {} },
    instructions: [
      'Control EasyEDA Pro through pcbpilot.',
      'For project.create, provide an explicit window and payload.friendlyName, without project/doc routing. For other mutations, provide project and doc.',
      'Inspect before editing and run schematic/PCB checks plus native DRC after editing.',
      'Do not bypass workflow gates or use force-unsafe in real projects.',
    ].join(' '),
  },
);

const commonRouteProperties = {
  project: {
    type: 'string',
    description: 'Existing EasyEDA project name or UUID. Required for mutations except project.create; omit for project.create.',
  },
  doc: {
    type: 'string',
    description: 'Existing schematic page or PCB name/UUID. Required for mutations except project.create; omit for project.create.',
  },
  window: {
    type: 'string',
    description: 'Connector windowId from pcbpilot_health. Required for project.create; otherwise use to resolve multi-window ambiguity.',
  },
  payload: {
    type: 'object',
    description: 'Typed action payload. Use pcbpilot_actions to inspect the action inputs.',
    additionalProperties: true,
  },
};

function domainTool(domain) {
  const domainActions = actions.filter((action) => action.domain === domain);
  return {
    name: `pcbpilot_${domain}`,
    title: `EasyEDA ${domain}`,
    description: `Run one typed ${domain} action through pcbpilot. Use pcbpilot_actions for input guidance.`,
    inputSchema: {
      type: 'object',
      properties: {
        action: {
          type: 'string',
          enum: domainActions.map((action) => action.name),
          description: 'Exact typed action name.',
        },
        ...commonRouteProperties,
      },
      required: ['action'],
      additionalProperties: false,
    },
    annotations: {
      readOnlyHint: domainActions.every((action) => !action.mutates),
      destructiveHint: domainActions.some((action) => action.mutates),
      idempotentHint: false,
      openWorldHint: false,
    },
  };
}

const tools = [
  {
    name: 'pcbpilot_health',
    title: 'EasyEDA connection health',
    description: 'Check the local daemon and connected EasyEDA Pro windows.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
    annotations: { readOnlyHint: true, destructiveHint: false, idempotentHint: true, openWorldHint: false },
  },
  {
    name: 'pcbpilot_actions',
    title: 'Discover EasyEDA actions',
    description: `Search the ${actions.length} typed EasyEDA actions and inspect inputs, mutation flags, and confirmation requirements.`,
    inputSchema: {
      type: 'object',
      properties: {
        domain: { type: 'string', enum: DOMAIN_NAMES },
        search: { type: 'string' },
        mutates: { type: 'boolean' },
      },
      additionalProperties: false,
    },
    annotations: { readOnlyHint: true, destructiveHint: false, idempotentHint: true, openWorldHint: false },
  },
  ...DOMAIN_NAMES.map(domainTool),
  {
    name: 'pcbpilot_blocks',
    title: 'EasyEDA circuit blocks',
    description: 'List, search, or show an embedded proven circuit block. Does not require a running daemon.',
    inputSchema: {
      type: 'object',
      properties: {
        operation: { type: 'string', enum: ['list', 'search', 'show'] },
        query: { type: 'string', description: 'Required for search.' },
        id: { type: 'string', description: 'Required for show.' },
      },
      required: ['operation'],
      additionalProperties: false,
    },
    annotations: { readOnlyHint: true, destructiveHint: false, idempotentHint: true, openWorldHint: false },
  },
  {
    name: 'pcbpilot_workflow',
    title: 'EasyEDA guarded workflow',
    description: 'Inspect or advance the persisted project design-flow state machine.',
    inputSchema: {
      type: 'object',
      properties: {
        operation: { type: 'string', enum: ['init', 'status', 'advance', 'confirm', 'reset'] },
        project: { type: 'string' },
        doc: { type: 'string' },
        reconcile: { type: 'boolean', description: 'For status: reconcile persisted state with the live document.' },
        minScore: { type: 'integer', minimum: 0, maximum: 100, description: 'For advance: minimum routability score.' },
        maxCrossings: { type: 'integer', minimum: -1, description: 'For advance: maximum ratline crossings; -1 is unlimited.' },
        confirmation: { type: 'string', enum: ['layout', 'outline'], description: 'Required for confirm.' },
        note: { type: 'string', description: 'Human review note recorded by confirm.' },
        resetAll: { type: 'boolean', description: 'For reset: clear every confirmation.' },
        resetFrom: { type: 'string', description: 'For reset: first stage to clear, inclusive.' },
      },
      required: ['operation', 'project'],
      additionalProperties: false,
    },
    annotations: { readOnlyHint: false, destructiveHint: true, idempotentHint: false, openWorldHint: false },
  },
];

server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools }));

server.setRequestHandler(CallToolRequestSchema, async (request) => {
  const { name, arguments: input = {} } = request.params;
  try {
    if (name === 'pcbpilot_health') {
      return toMcpResult(await runEasyeda(['daemon', 'health'], 30_000));
    }
    if (name === 'pcbpilot_actions') {
      const filtered = filterActions(actions, input);
      return toMcpResult({ ok: true, result: { count: filtered.length, actions: filtered } });
    }
    if (name === 'pcbpilot_blocks') {
      return toMcpResult(await runEasyeda(buildBlocksArgs(input), 30_000));
    }
    if (name === 'pcbpilot_workflow') {
      return toMcpResult(await runEasyeda(buildWorkflowArgs(input)));
    }
    if (name.startsWith('pcbpilot_')) {
      const domain = name.slice('pcbpilot_'.length);
      if (!DOMAIN_NAMES.includes(domain)) throw new Error(`unknown EasyEDA domain tool: ${name}`);
      const action = byName.get(input.action);
      if (!action || action.domain !== domain) {
        throw new Error(`action ${input.action || '(missing)'} does not belong to domain ${domain}`);
      }
      return toMcpResult(await runEasyeda(buildActionCallArgs(action, input)));
    }
    throw new Error(`unknown tool: ${name}`);
  }
  catch (error) {
    return toMcpResult({ ok: false, error: { message: error.message } });
  }
});

await server.connect(new StdioServerTransport());
