from __future__ import annotations

import hashlib
import json
from pathlib import Path


ROOT = Path(r"E:\Dev\ownward\.tmp\vector-model-evaluation")
OUTPUT = ROOT / "data" / "ownward-quality-supplement-v1"


CASES = [
    {
        "category": "deep_paraphrase",
        "scope": "method",
        "language": "zh",
        "query": "我之前为了不在出门时漏带门禁卡，最后固定下来的办法是什么？",
        "positive": "手机提醒总在走出楼门后才被看到。后来我把门禁卡固定夹进钥匙包，回家开门后立刻放回原位。",
        "negatives": [
            "我试过在手机上设置出门提醒，但经常顺手划掉，实际没有解决忘带门禁卡的问题。",
            "有一阵我把门禁卡塞进当天穿的外套，换衣服时还是会忘，因此没有继续这样做。",
            "门边贴着一张检查燃气和窗户的便签，出门前看一眼能少些担心。",
        ],
        "rationale": "答案是已经稳定采用的钥匙包方案，而不是尝试过但放弃的提醒、口袋或其他出门检查。",
    },
    {
        "category": "deep_paraphrase",
        "scope": "method",
        "language": "zh",
        "query": "写长文总拖到最后，我后来真正采用的启动办法是什么？",
        "positive": "现在开新文章时，我先关掉资料页，用二十分钟写一份很粗糙的两百字骨架；有了可修改的东西，再去补证据。",
        "negatives": [
            "番茄钟试了两周，计时本身反而让我频繁查看进度，没有改善长文启动困难。",
            "我给写作资料建立了分类收藏夹，里面按主题保存了很多以后可能引用的链接。",
            "交稿前最后一遍只检查论点与证据是否对应，不再同时调整排版。",
        ],
        "rationale": "答案是先写粗糙骨架再补证据；其他记录分别是失败尝试、资料整理和收尾方法。",
    },
    {
        "category": "deep_paraphrase",
        "scope": "work",
        "language": "zh",
        "query": "开会时讨论容易越跑越远，我给自己定的处理方式是什么？",
        "positive": "以后有人带入新话题时，我先复述本次必须作出的决定，再把无关问题记到停车场清单，会后另约时间。",
        "negatives": [
            "会议邀请里应提前附上议程和材料，参会者最好在前一天读完。",
            "线上会议时我会关闭聊天软件通知，避免说到一半被弹窗打断。",
            "录音转写适合会后补会议纪要，但不能替代现场确认负责人和截止时间。",
        ],
        "rationale": "答案是重申决策目标并暂存支线话题，其他记录只涉及准备、专注或纪要。",
    },
    {
        "category": "deep_paraphrase",
        "scope": "habit",
        "language": "zh",
        "query": "晚上总忍不住刷手机影响睡眠，我最后用了什么安排？",
        "positive": "我把充电线移到客厅，十点半后手机留在那里，卧室改用普通闹钟；这样上床后就没有设备可刷。",
        "negatives": [
            "应用限额很容易被我点掉，连续几晚失效后就不再把它当作主要办法。",
            "屏幕调成暖色以后眼睛没那么刺，但睡前浏览的时间并没有缩短。",
            "早上起床后先喝水再看消息，可以避免一醒来就在床上停留太久。",
        ],
        "rationale": "答案是把手机物理移出卧室；软件限额和暖色模式均未解决问题。",
    },
    {
        "category": "decision_polarity",
        "scope": "decision",
        "language": "zh",
        "query": "数据库迁移时，哪种做法被明确排除了？",
        "positive": "评审后决定不让新旧系统长时间双写：两边失败语义不同，持续双写会制造难以核对的分叉数据。",
        "negatives": [
            "迁移前先做影子读取，对比新旧结果但不影响线上返回，这是已经接受的验证步骤。",
            "正式切换采用短暂停写、校验水位后一次转向新库，回滚窗口保留十五分钟。",
            "历史数据按日期分批回填，每批完成后核对数量、校验和与抽样内容。",
        ],
        "rationale": "查询要求被排除的方案，只有长期双写被明确否决。",
    },
    {
        "category": "decision_polarity",
        "scope": "health",
        "language": "zh",
        "query": "膝盖不舒服以后，医生允许我继续做哪种运动？",
        "positive": "复诊时医生同意我在不诱发疼痛的前提下继续游泳，并要求先缩短到每次二十分钟。",
        "negatives": [
            "医生让我暂时停止跑步，尤其不要做下坡冲刺，等肿胀消失后再评估。",
            "骑车是否恢复要看屈膝角度，当前阶段还没有得到可以开始的确认。",
            "深蹲训练被取消两周，力量练习只保留不会让膝盖受力的上肢部分。",
        ],
        "rationale": "只有游泳被明确允许继续，其他运动被停止或尚待确认。",
    },
    {
        "category": "decision_polarity",
        "scope": "work",
        "language": "zh",
        "query": "团队对自动发布最后定下来的边界是什么？",
        "positive": "构建和预发布检查可以自动完成，但生产发布必须由当班负责人查看差异并手动确认，暂不做无人值守上线。",
        "negatives": [
            "有人建议合并主分支后自动发布生产，以减少等待时间，这个提议仍停留在讨论稿里。",
            "预发布环境每天自动部署一次，失败时由机器人在群里通知，不需要人工触发。",
            "回滚脚本已自动化，执行前仍要选择目标版本并记录事故编号。",
        ],
        "rationale": "最终边界是检查自动化、生产人工确认；无人值守生产发布只是未采纳的提议。",
    },
    {
        "category": "decision_polarity",
        "scope": "experience",
        "language": "zh",
        "query": "那次报销被退回，后来确认的真正原因是什么？",
        "positive": "财务复核后确认，发票本身没有问题，退回是因为审批单上的项目编码与预算科目不一致。",
        "negatives": [
            "我最初以为含税金额填错了，重新计算后发现与发票完全一致。",
            "同事怀疑开票日期超期，但财务确认日期仍在允许范围内。",
            "电子签名显示正常，退回通知也没有要求重新签署。",
        ],
        "rationale": "项目编码不一致才是确认后的原因，其余均为被排除的猜测。",
    },
    {
        "category": "context_disambiguation",
        "scope": "family",
        "language": "zh",
        "query": "给父亲设置的服药提醒采用了什么方式？",
        "positive": "父亲不常看日历通知，我把提醒改成早餐后由客厅音箱直接播报，并在药盒旁放当天日期卡。",
        "negatives": [
            "我自己的维生素放在办公桌抽屉，午饭后的日历通知响起时顺手吃。",
            "母亲的复诊日期由家庭群置顶，提前一周和前一天各提醒一次。",
            "客厅音箱每天早上播天气，音量调低后不会吵到还在睡觉的人。",
        ],
        "rationale": "查询限定父亲服药，答案是音箱播报加日期卡，不能混入自己的日历提醒或其他家庭事项。",
    },
    {
        "category": "context_disambiguation",
        "scope": "method",
        "language": "zh",
        "query": "家庭照片最终采用哪种备份方式，而不是代码仓库的做法？",
        "positive": "家庭照片每月复制到一块加密移动硬盘，硬盘平时断开存放；每季度随机恢复一个相册确认可读。",
        "negatives": [
            "代码提交后推送到两个远程仓库，主仓库不可用时可以从镜像继续工作。",
            "手机相册自动同步只是方便跨设备查看，不再把同步状态当成唯一备份。",
            "项目数据库每天导出增量文件，保留七天后自动轮换。",
        ],
        "rationale": "查询限定家庭照片，正确记录是加密移动硬盘和恢复验证，其他是代码、同步或数据库策略。",
    },
    {
        "category": "context_disambiguation",
        "scope": "social_experience",
        "language": "zh",
        "query": "和设计组沟通时，我提醒自己不要用哪种开场方式？",
        "positive": "设计评审里不要一开口就说“这个不对”；先说明用户在哪一步受阻，再一起看当前方案为什么没有解决。",
        "negatives": [
            "和运维讨论事故时，开场先确认时间线和受影响范围，避免直接猜测责任方。",
            "给直属同事反馈时，先讲观察到的行为，再说明它造成的具体影响。",
            "设计稿评论尽量标在对应界面位置，集中问题另写一段总体说明。",
        ],
        "rationale": "查询同时限定设计组与开场方式，只有避免直接说“这个不对”的记录满足。",
    },
    {
        "category": "context_disambiguation",
        "scope": "experience",
        "language": "zh",
        "query": "家里网络卡顿最后查出的瓶颈在哪里？",
        "positive": "入户带宽正常，电脑直连路由器也正常；最后发现书房旧交换机的上联口只协商到 100Mbps，更换网线后恢复千兆。",
        "negatives": [
            "办公室无线网络曾因同信道干扰频繁抖动，把接入点改到其他信道后稳定。",
            "家里有一次网页打开慢是运营商 DNS 超时，切换解析服务器后当天恢复。",
            "晚高峰测速下降曾让我怀疑运营商限速，但连续三天直连测试没有复现。",
        ],
        "rationale": "答案是书房交换机上联只协商到百兆；其他记录属于不同地点、不同故障或被排除的猜测。",
    },
    {
        "category": "buried_evidence",
        "scope": "decision",
        "language": "zh",
        "query": "买显示器时，我真正不能妥协的条件是什么？",
        "positive": "看了几款屏幕后，我发现高刷新率和接口数量都可以让步。每天要连续读代码和文档，最终留下的硬条件只有正常坐姿下文字必须清晰、长时间看不发虚。支架以后还能另配。",
        "negatives": [
            "候选显示器有高刷新率、多个视频接口和原厂升降支架，参数表看起来很完整。",
            "桌面宽度最多容纳二十七英寸屏幕，因此购买前需要再次测量底座占用。",
            "原厂支架不能旋转，但我已经有一只兼容的显示器臂，这点不影响选择。",
        ],
        "rationale": "长文本明确把文字清晰度定义为唯一硬条件，其他信息只是参数、空间限制或可替代配件。",
    },
    {
        "category": "buried_evidence",
        "scope": "experience",
        "language": "zh",
        "query": "上次旅行为什么临时改坐高铁？",
        "positive": "机票并没有取消，价格也没变化。问题是前序航班延误后只剩四十分钟转机，而同行人行动不便，机场又要换航站楼。评估后我们退掉机票，改成可以直达的高铁。",
        "negatives": [
            "高铁票临出发前仍有余票，二等座价格比当天新买机票便宜一些。",
            "另一趟旅行因为暴雨导致航班取消，航空公司安排到了第二天。",
            "出发前我查过机场换乘路线，两个航站楼之间需要乘摆渡车。",
        ],
        "rationale": "真正触发改签的是延误后转机风险与同行人的行动条件，不是票价、取消或单独的换楼信息。",
    },
    {
        "category": "buried_evidence",
        "scope": "learning",
        "language": "zh",
        "query": "学统计那段时间，哪种做法真正让我理解得更牢？",
        "positive": "我原来只是反复看公式，做题时仍不知道每个量代表什么。后来每学一个概念，先手工造一组很小的数据，自己算一遍，再用代码验证；从那以后能解释结果为什么变化。",
        "negatives": [
            "统计课程的讲义按章节整理好了，所有公式都抄进了同一个笔记本。",
            "我收藏了几门评价很高的视频课，但同时切换老师会让符号体系更加混乱。",
            "考试前集中刷历年题能熟悉题型，不过遇到换一种问法时仍然容易卡住。",
        ],
        "rationale": "答案是自造小数据、手算再用代码验证；抄公式、收藏课程和刷题都没有带来同等理解。",
    },
    {
        "category": "buried_evidence",
        "scope": "lesson",
        "language": "zh",
        "query": "那次和朋友争执后，我认为自己最该改的是什么？",
        "positive": "回头看，她讲难受时我连续给了三个解决方案，还追问为什么不马上行动。那些建议不一定错，但我跳过了她当时需要被理解的部分。下次先确认感受和她想要的是倾听还是建议。",
        "negatives": [
            "争执发生在很晚的时候，双方都累了，以后重要话题尽量不要拖到睡前。",
            "我整理了三个可能的解决办法，其中第二个成本最低，也最容易在一周内执行。",
            "她后来解释，沉默并不是生气，只是需要一点时间把自己的想法说清楚。",
        ],
        "rationale": "核心反思是过早给方案、没有先理解需求；时间、方案成本和对方沉默都不是自己的主要改进点。",
    },
    {
        "category": "english_semantics",
        "scope": "work",
        "language": "en",
        "query": "What finally stopped the weekly report from consuming most of Friday afternoon?",
        "positive": "I started adding decisions and evidence to a running note as they happened. On Friday I now edit that note into the report instead of reconstructing the whole week from chat history.",
        "negatives": [
            "I moved the report deadline from Friday noon to Friday evening, which created more room but did not reduce the work itself.",
            "The report template has shorter headings now, although gathering the facts still used to take several hours.",
            "Chat history is retained for ninety days and can be searched when someone needs an old discussion.",
        ],
        "rationale": "The adopted solution is continuous evidence capture; deadline and template changes did not remove the reconstruction work.",
    },
    {
        "category": "english_semantics",
        "scope": "work",
        "language": "en",
        "query": "What did I decide to do with receipts immediately after buying something for work?",
        "positive": "As soon as a work purchase is complete, I photograph the receipt and add the project code to the expense draft. Waiting until month-end is what made the context impossible to reconstruct.",
        "negatives": [
            "Paper receipts stay in the blue envelope until the reimbursement has been paid, in case finance asks for the original.",
            "Personal purchases are entered in the household budget once a week and do not need a project code.",
            "I used to collect all work receipts and reconstruct their purpose on the last day of the month, but too many details were missing.",
        ],
        "rationale": "The adopted immediate action is photographing the receipt and recording its project code; storage, personal budgeting, and the abandoned month-end process are not the answer.",
    },
    {
        "category": "english_semantics",
        "scope": "decision",
        "language": "en",
        "query": "Why did I turn down the earlier apartment?",
        "positive": "The viewing was quiet in the afternoon, but a second visit at night revealed freight trains passing behind the building every twenty minutes. That recurring noise was the reason I declined it.",
        "negatives": [
            "The earlier apartment was slightly cheaper and included a storage room in the basement.",
            "Another apartment was rejected because the commute required two transfers and took more than an hour.",
            "The agent offered to replace the bedroom windows, but no written schedule or cost estimate was provided.",
        ],
        "rationale": "The decisive reason was recurring nighttime freight noise, not price, another apartment's commute, or an unconfirmed window offer.",
    },
    {
        "category": "english_semantics",
        "scope": "method",
        "language": "en",
        "query": "What should I do when a code-review comment feels too vague to act on?",
        "positive": "Ask for the failing case or the behavior the reviewer expects, then restate the requested change in concrete terms before editing the code.",
        "negatives": [
            "Collect all review comments before pushing the next revision so reviewers do not receive many small updates.",
            "If a comment concerns formatting, run the repository formatter rather than adjusting nearby lines by hand.",
            "A review thread should be marked resolved only after the corresponding change is visible in the latest diff.",
        ],
        "rationale": "The query is about clarifying an ambiguous comment; only asking for a failing case and restating the behavior addresses that ambiguity.",
    },
    {
        "category": "cross_language",
        "scope": "work",
        "language": "zh_to_en",
        "query": "给海外客户演示前，我最终决定怎么处理产品术语？",
        "positive": "For the client demo, I will keep the official product terms in English and explain each one once in plain language, instead of inventing translated names that differ from the interface.",
        "negatives": [
            "The slides use shorter sentences now, and every section begins with the customer problem rather than a feature list.",
            "我曾考虑把所有术语都翻成中文再口头解释，但这样会与客户看到的英文界面不一致。",
            "The rehearsal showed that switching windows took too long, so all examples were moved into one browser profile.",
        ],
        "rationale": "正确记录是保留官方英文术语并做一次通俗解释；全量翻译是被放弃的方案。",
    },
    {
        "category": "cross_language",
        "scope": "habit",
        "language": "en_to_zh",
        "query": "What made the morning exercise routine sustainable?",
        "positive": "真正坚持下来以后，我不再要求每天完成整套训练，只规定穿上鞋出门十分钟；状态好再继续，状态差也算完成。",
        "negatives": [
            "我买了一套新的训练服，刚开始的一周确实更愿意早起，但新鲜感很快就过去了。",
            "晚上提前把水杯放在桌上，可以减少早晨临时找东西的时间。",
            "The original plan required forty-five minutes every morning and was abandoned after several missed days.",
        ],
        "rationale": "The sustainable change was lowering the minimum commitment to ten minutes, not clothing, preparation, or the abandoned full routine.",
    },
    {
        "category": "cross_language",
        "scope": "experience",
        "language": "zh_to_en",
        "query": "上次排查线上延迟时，最终确认的根因是什么？",
        "positive": "The database was healthy. Latency came from a retry loop in the client: requests that had already succeeded were sent again when the acknowledgement arrived just after the timeout boundary.",
        "negatives": [
            "The slow-request dashboard initially pointed to the database because most delayed calls ended at the storage service.",
            "我们把连接池从二十调到四十做过对照，延迟分布几乎没有变化，因此恢复了原配置。",
            "A separate incident was caused by DNS failures between two regions and produced connection errors rather than duplicate requests.",
        ],
        "rationale": "根因是客户端超时边界触发的重复重试，数据库、连接池和另一次 DNS 故障均不是答案。",
    },
    {
        "category": "cross_language",
        "scope": "lesson",
        "language": "en_to_zh",
        "query": "What did I learn to do before agreeing to a family request?",
        "positive": "家人临时提出帮忙时，我以后先确认具体要做什么、最晚什么时候需要，再看自己的安排后答复，不在电话里因为内疚立刻承诺。",
        "negatives": [
            "上次答应帮忙后，我把原定的休息计划挪到了第二天，事情最后还是完成了。",
            "家人有时只是想先讨论可能性，并不代表当天就需要一个肯定答复。",
            "I wrote a shared list of recurring household tasks so everyone can see which items still need an owner.",
        ],
        "rationale": "The lesson is to clarify scope and deadline, check capacity, and only then answer; the other notes do not state that decision rule.",
    },
]


def write_jsonl(path: Path, values: list[dict[str, object]]) -> None:
    with path.open("w", encoding="utf-8", newline="\n") as handle:
        for value in values:
            handle.write(json.dumps(value, ensure_ascii=False, sort_keys=True) + "\n")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> None:
    if len(CASES) != 24:
        raise RuntimeError(f"补测案例数异常：{len(CASES)}")
    OUTPUT.mkdir(parents=True, exist_ok=True)
    queries: list[dict[str, object]] = []
    corpus: list[dict[str, object]] = []
    qrels: list[dict[str, object]] = []
    adjudication: list[dict[str, object]] = []

    for index, case in enumerate(CASES, start=1):
        query_id = f"sq-{index:03d}"
        documents = [case["positive"], *case["negatives"]]
        if len(documents) != 4 or len(set(documents)) != 4:
            raise RuntimeError(f"案例 {query_id} 的候选文档无效")
        positive_slot = (index * 3 + 1) % 4
        ordered = list(documents[1:])
        ordered.insert(positive_slot, documents[0])
        document_ids: list[str] = []
        positive_id = ""
        for slot, text in enumerate(ordered):
            document_id = f"sd-{index:03d}-{slot + 1}"
            document_ids.append(document_id)
            if text == case["positive"]:
                positive_id = document_id
            corpus.append(
                {
                    "id": document_id,
                    "text": text,
                    "world_id": f"sw-{index:03d}",
                }
            )
        queries.append(
            {
                "id": query_id,
                "text": case["query"],
                "category": case["category"],
                "scope": case["scope"],
                "language": case["language"],
            }
        )
        qrels.append({"query_id": query_id, "document_id": positive_id, "relevance": 1})
        adjudication.append(
            {
                "query_id": query_id,
                "relevant_document_id": positive_id,
                "candidate_document_ids": document_ids,
                "rationale": case["rationale"],
            }
        )

    if len({item["text"] for item in queries}) != len(queries):
        raise RuntimeError("补测查询存在重复")
    if len({item["text"] for item in corpus}) != len(corpus):
        raise RuntimeError("补测文档存在重复")

    write_jsonl(OUTPUT / "queries.jsonl", queries)
    write_jsonl(OUTPUT / "corpus.jsonl", corpus)
    write_jsonl(OUTPUT / "qrels.jsonl", qrels)
    write_jsonl(OUTPUT / "adjudication.jsonl", adjudication)
    manifest = {
        "dataset": "ownward-quality-supplement-v1",
        "status": "frozen_before_candidate_results",
        "query_count": len(queries),
        "document_count": len(corpus),
        "documents_per_query_case": 4,
        "category_counts": {
            category: sum(1 for item in queries if item["category"] == category)
            for category in sorted({str(item["category"]) for item in queries})
        },
        "language_counts": {
            language: sum(1 for item in queries if item["language"] == language)
            for language in sorted({str(item["language"]) for item in queries})
        },
        "design": [
            "人工逐条策划，不使用模板扩写或唯一名称匹配",
            "每条查询只有一个可由文本直接判定的最佳答案",
            "每条配置三个同主题强干扰项，覆盖否定、弃用、上下文错配和表层词面诱导",
            "只测试单节点语义检索，不测试图推理、时间排序或外部智能体能力",
            "中文为主，英文与中英交叉作为次要但必要覆盖",
        ],
        "files": {},
    }
    for name in ("queries.jsonl", "corpus.jsonl", "qrels.jsonl", "adjudication.jsonl"):
        path = OUTPUT / name
        manifest["files"][name] = {"bytes": path.stat().st_size, "sha256": sha256(path)}
    manifest_path = OUTPUT / "manifest.json"
    manifest_path.write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )
    base_config = json.loads((ROOT / "state" / "frozen-config-v2.json").read_text(encoding="utf-8"))
    supplement_config = {
        "execution": base_config["execution"],
        "freeze_id": "ownward-quality-supplement-2026-08-20-v1",
        "freeze_status": "frozen_before_candidate_results",
        "models": base_config["models"],
        "ownward_track": {
            "categories": sorted(manifest["category_counts"]),
            "corpus_size": len(corpus),
            "dataset_manifest_sha256": sha256(manifest_path),
            "metrics": ["top1_accuracy", "mrr_at_10", "recall_at_5"],
            "queries_per_category": 4,
            "query_count": len(queries),
            "selection": "compare top1 accuracy, then MRR@10, then Recall@5; inspect paired failures before resolving conflicting indicators",
        },
    }
    config_path = ROOT / "state" / "quality-supplement-v1-config.json"
    config_path.write_text(
        json.dumps(supplement_config, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
        newline="\n",
    )
    print(json.dumps(manifest, ensure_ascii=False, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
