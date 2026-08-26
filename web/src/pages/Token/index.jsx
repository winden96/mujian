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

import React from 'react';
import { KeyRound } from 'lucide-react';
import TokensTable from '../../components/table/tokens';
import '../mujian.css';

const Token = () => {
  return (
    <main className='mujian-page'>
      <section className='mujian-page-header'>
        <div>
          <div className='mujian-eyebrow'>
            <KeyRound size={14} /> OpenAI Compatible
          </div>
          <h1>API Key</h1>
          <p>自己创建密钥，用来调用幕间的接口。每个 Key 可单独停用或删除。</p>
        </div>
      </section>
      <TokensTable />
    </main>
  );
};

export default Token;
