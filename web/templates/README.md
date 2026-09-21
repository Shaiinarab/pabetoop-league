# قالب‌های عمومی — راهنمای نگارش (Template Authoring Notes)

این پوشه قالب‌های سمت‌سرور صفحات عمومی است (TASK-005). قراردادها:

## ساختار
- `public_base.html` — پایه: `topbar`، ناوبری (خانه + رده‌های سنی)، `footer`؛ بلوک‌های `title` و `content`.
- `partials/standings_table.html` — جدول رده‌بندی؛ `{{define "standings_table"}}`.
- `partials/match_row.html` — سطر مسابقه؛ `{{define "match_row"}}`، فیلدهای nil-safe.
- `home.html`، `age_group.html`، `competition.html` — صفحات کامل؛ هرکدام `{{define "…"}}` با نام صفحه.

## قراردادهای سرویرنده (فراهم‌کنندهٔ داده — Lead)
- هندلرها با `template.ParseFS` + FuncMap زیر استفاده می‌کنند:
  `toFa` (ارقام لاتین→فارسی)، `jalaliDate` (ISO→«۱۴۰۵/۰۷/۲۰»، nil-safe)، `jalaliLong` (ISO→«۲۰ مهر ۱۴۰۵»، nil-safe).
- دادهٔ هر صفحه مطابق کامنت فارسی ابتدای همان فایل (قرارداد داده).
- `.IsHome` فقط در صفحهٔ خانه true است (حالت فعال ناوبری).

## قواعد رعایت‌شده
- تمام متن‌های کاربر فارسی؛ ارقام همه از `toFa` می‌گذرند (بدون رقم لاتین در متن نمایان).
- هیچ class خارج از `web/static/css/main.css` استفاده نشده؛ هیچ استایل درون‌خطی وجود ندارد.
- htmx 4: هیچ attribute حذف‌شدهٔ ۴ (hx-ext، hx-vars، hx-inherit، hx-disinherit، hx-params) به‌کار نرفته؛
  صفحات عمومی بدون htmx هم کامل رندر می‌شوند (progressive enhancement فقط در admin).
- تاریخ‌ها فقط از funcmap؛ قالب‌ها هیچ محاسبهٔ تاریخ انجام نمی‌دهند (D9).
