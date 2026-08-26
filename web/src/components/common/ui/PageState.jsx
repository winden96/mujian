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
import PropTypes from 'prop-types';
import { LoaderCircle } from 'lucide-react';
import './page-state.css';

const PageState = ({
  eyebrow,
  title,
  description,
  icon,
  actions,
  busy = false,
}) => (
  <main
    className='mujian-page-state'
    aria-busy={busy || undefined}
    aria-live={busy ? 'polite' : undefined}
  >
    <section className='mujian-page-state__card'>
      <div className='mujian-page-state__icon' aria-hidden='true'>
        {busy ? <LoaderCircle className='mujian-page-state__spinner' /> : icon}
      </div>
      {eyebrow && <p className='mujian-page-state__eyebrow'>{eyebrow}</p>}
      <h1>{title}</h1>
      {description && (
        <p className='mujian-page-state__description'>{description}</p>
      )}
      {actions && <div className='mujian-page-state__actions'>{actions}</div>}
    </section>
  </main>
);

PageState.propTypes = {
  eyebrow: PropTypes.node,
  title: PropTypes.node.isRequired,
  description: PropTypes.node,
  icon: PropTypes.node,
  actions: PropTypes.node,
  busy: PropTypes.bool,
};

export default PageState;
