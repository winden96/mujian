/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

export const withRuntimeServer = (spec, gatewayAddress) => ({
  ...spec,
  servers: [
    {
      url: gatewayAddress,
      description: '当前幕间网关',
    },
  ],
});

export const omitRequestCredentials = (request) => {
  request.credentials = 'omit';
  return request;
};

export const createQuickstarts = (apiBaseURL) => [
  {
    id: 'curl',
    label: 'cURL',
    language: 'bash',
    code: `curl "${apiBaseURL}/chat/completions" \\
  -H "Authorization: Bearer $MUJIAN_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "你好，幕间"}]
  }'`,
  },
  {
    id: 'python',
    label: 'Python',
    language: 'python',
    code: `from openai import OpenAI

client = OpenAI(
    api_key="YOUR_MUJIAN_API_KEY",
    base_url="${apiBaseURL}",
)

response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "你好，幕间"}],
)
print(response.choices[0].message.content)`,
  },
  {
    id: 'node',
    label: 'Node.js',
    language: 'javascript',
    code: `const response = await fetch(
  "${apiBaseURL}/chat/completions",
  {
    method: "POST",
    headers: {
      "Authorization": "Bearer YOUR_MUJIAN_API_KEY",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      model: "gpt-4o-mini",
      messages: [{ role: "user", content: "你好，幕间" }],
    }),
  },
);

console.log(await response.json());`,
  },
];
