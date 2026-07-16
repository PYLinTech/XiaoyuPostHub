import { generate, getRgbStr } from '@arco-design/color';

function changeTheme(theme, themeColor) {
  if (theme === 'dark') {
    document.body.setAttribute('arco-theme', 'dark');
  } else {
    document.body.removeAttribute('arco-theme');
  }

  generate(themeColor, { list: true, dark: theme === 'dark' }).forEach(
    (color, index) => {
      document.body.style.setProperty(
        `--arcoblue-${index + 1}`,
        getRgbStr(color)
      );
    }
  );
}

export default changeTheme;
