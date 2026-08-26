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

import { createContext, useCallback, useContext, useEffect } from 'react';

const ThemeContext = createContext(null);
export const useTheme = () => useContext(ThemeContext);

const ActualThemeContext = createContext(null);
export const useActualTheme = () => useContext(ActualThemeContext);

const SetThemeContext = createContext(null);
export const useSetTheme = () => useContext(SetThemeContext);

const LIGHT_THEME = 'light';

export const ThemeProvider = ({ children }) => {
  // 产品只使用浅色主题。旧的 localStorage 偏好保留原样，但不再参与渲染。
  useEffect(() => {
    const body = document.body;
    const root = document.documentElement;

    body.setAttribute('theme-mode', LIGHT_THEME);
    root.classList.remove('dark');
    root.style.colorScheme = LIGHT_THEME;

    return () => {
      root.classList.remove('dark');
    };
  }, []);

  // 保留旧调用方的函数契约。固定浅色后，调用不再改写偏好。
  const setTheme = useCallback(() => {}, []);

  return (
    <SetThemeContext.Provider value={setTheme}>
      <ActualThemeContext.Provider value={LIGHT_THEME}>
        <ThemeContext.Provider value={LIGHT_THEME}>
          {children}
        </ThemeContext.Provider>
      </ActualThemeContext.Provider>
    </SetThemeContext.Provider>
  );
};
