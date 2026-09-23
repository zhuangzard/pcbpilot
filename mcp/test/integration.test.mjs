import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';

const packageDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const serverPath = process.env.PCBPILOT_MCP_SERVER || path.join(packageDir, 'src', 'server.mjs');

test('stdio MCP initializes, lists tools, and invokes offline discovery', async () => {
  assert.ok(process.env.PCBPILOT_BIN, 'PCBPILOT_BIN must point to the local CLI for integration tests');
  const transport = new StdioClientTransport({
    command: process.execPath,
    args: [serverPath],
    cwd: packageDir,
    env: { ...process.env, PCBPILOT_BIN: process.env.PCBPILOT_BIN },
  });
  const client = new Client({ name: 'pcbpilot-mcp-test', version: '1.0.0' });

  try {
    await client.connect(transport);
    const listed = await client.listTools();
    assert.equal(listed.tools.length, 11);
    assert.ok(listed.tools.some((tool) => tool.name === 'pcbpilot_pcb'));
    assert.ok(!listed.tools.some((tool) => tool.name === 'pcbpilot_debug'));

    const allActions = await client.callTool({
      name: 'pcbpilot_actions',
      arguments: {},
    });
    assert.equal(allActions.isError, false);
    assert.ok(!allActions.structuredContent.actions.some((action) => action.domain === 'debug'));

    const discovered = await client.callTool({
      name: 'pcbpilot_actions',
      arguments: { domain: 'schematic', search: 'check', mutates: false },
    });
    assert.equal(discovered.isError, false);
    assert.ok(Array.isArray(discovered.structuredContent.actions));
    assert.ok(discovered.structuredContent.actions.some((action) => action.name === 'schematic.check'));

    const rejectedMutation = await client.callTool({
      name: 'pcbpilot_schematic',
      arguments: { action: 'schematic.page.create', payload: { name: 'Unsafe' } },
    });
    assert.equal(rejectedMutation.isError, true);
    assert.match(rejectedMutation.content[0].text, /requires both project and doc/);

    for (const routing of [{}, { window: 'home-window', doc: 'tab_page1' }, { window: 'home-window', project: 'Future project' }]) {
      const rejectedCreate = await client.callTool({
        name: 'pcbpilot_project',
        arguments: { action: 'project.create', payload: { friendlyName: 'Must not be created' }, ...routing },
      });
      assert.equal(rejectedCreate.isError, true);
      assert.match(rejectedCreate.content[0].text, /requires an explicit window|does not accept project or doc/);
    }

    const blocks = await client.callTool({
      name: 'pcbpilot_blocks',
      arguments: { operation: 'search', query: 'led' },
    });
    assert.equal(blocks.isError, false);
  }
  finally {
    await client.close();
  }
});
